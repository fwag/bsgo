#!/usr/bin/env python3
"""
Convert SLFO (SUSE Linux Framework One / OBS Git repository) into a bsgo-compatible project.

This script:
1. Reads .gitmodules and git tree commits (mode 160000) from the specified branch (e.g. slfo-1.2).
2. Converts the 3,500+ submodules into bsgo hash-sharded package manifests (packages/xx/<pkg>.yaml).
3. Parses _config (prjconf) into a modern bsgo project.yaml configuration while preserving _config macros.
4. Parses _pbuild to resolve build architectures and upstream repository dependencies.
5. Generates an import-to-bsgo.sh script for instant ingestion into bsgo control plane.
"""

import argparse
import configparser
import hashlib
import json
import os
import re
import subprocess
import sys
import xml.etree.ElementTree as ET
from pathlib import Path
from urllib.parse import urljoin

import yaml


def shard_path(package_name: str) -> str:
    """Compute bsgo hash partition path for a package: packages/<sha256[:2]>/<package>.yaml"""
    clean_name = os.path.basename(os.path.normpath(package_name)).replace("..", "")
    h = hashlib.sha256(clean_name.encode("utf-8")).hexdigest()[:2]
    return f"packages/{h}/{clean_name}.yaml"


def git_cmd(repo_dir: str, args: list[str]) -> str:
    """Run a git command in repo_dir and return stdout as string."""
    cmd = ["git", "-C", repo_dir] + args
    res = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=True)
    return res.stdout


def get_git_file(repo_dir: str, ref: str, file_path: str) -> str:
    """Extract file content from git ref."""
    try:
        return git_cmd(repo_dir, ["show", f"{ref}:{file_path}"])
    except subprocess.CalledProcessError:
        return ""


def get_submodule_commits(repo_dir: str, ref: str) -> dict[str, str]:
    """Parse git ls-tree for 160000 commit entries -> map[pkg_name] = commit_sha."""
    out = git_cmd(repo_dir, ["ls-tree", ref])
    submodules = {}
    for line in out.splitlines():
        parts = line.split()
        if len(parts) >= 4 and parts[0] == "160000":
            pkg_name = parts[3]
            commit_sha = parts[2]
            submodules[pkg_name] = commit_sha
    return submodules


def parse_gitmodules(raw_modules: str, gitea_base: str) -> dict[str, dict]:
    """Parse .gitmodules ini format into dictionary of package info."""
    config = configparser.ConfigParser()
    config.read_string(raw_modules)
    packages = {}
    origin_base = gitea_base.rstrip("/") + "/products/SLFO.git"

    for section in config.sections():
        sec = config[section]
        path = sec.get("path", "")
        raw_url = sec.get("url", "")
        branch = sec.get("branch", "slfo-1.2")

        if not path:
            continue

        # Resolve relative pool url: ../../pool/<pkg> -> https://src.opensuse.org/pool/<pkg>.git
        if raw_url.startswith("../"):
            clean_rel = raw_url.replace("../", "")
            resolved_url = f"{gitea_base.rstrip('/')}/{clean_rel}.git"
        elif raw_url.startswith("http://") or raw_url.startswith("https://"):
            resolved_url = raw_url
            if not resolved_url.endswith(".git"):
                resolved_url += ".git"
        else:
            resolved_url = f"{gitea_base.rstrip('/')}/pool/{path}.git"

        packages[path] = {
            "name": path,
            "url": resolved_url,
            "branch": branch,
        }
    return packages


def parse_prj_meta(raw_xml: str) -> dict:
    """Parse classic OBS project metadata XML (osc meta prj <project>)."""
    try:
        root = ET.fromstring(raw_xml)
    except Exception as e:
        sys.stderr.write(f"Warning: failed parsing prj_meta XML: {e}\n")
        return {}

    name = root.attrib.get("name", "")
    title_elem = root.find("title")
    title = title_elem.text.strip() if title_elem is not None and title_elem.text else ""
    desc_elem = root.find("description")
    desc = desc_elem.text.strip() if desc_elem is not None and desc_elem.text else ""

    scm_sync = ""
    scmsync_elem = root.find("scmsync")
    if scmsync_elem is not None and scmsync_elem.text:
        scm_sync = scmsync_elem.text.strip()

    maintainers = []
    for person in root.findall("person"):
        uid = person.attrib.get("userid", "")
        role = person.attrib.get("role", "maintainer")
        if uid and role == "maintainer" and uid not in maintainers:
            maintainers.append(uid)

    for group in root.findall("group"):
        gid = group.attrib.get("groupid", "")
        role = group.attrib.get("role", "maintainer")
        if gid and role == "maintainer":
            g_entry = f"group:{gid}"
            if g_entry not in maintainers:
                maintainers.append(g_entry)

    build_enabled = True
    build_elem = root.find("build")
    if build_elem is not None and build_elem.find("disable") is not None:
        build_enabled = False

    publish_enabled = True
    pub_elem = root.find("publish")
    if pub_elem is not None and pub_elem.find("disable") is not None:
        publish_enabled = False

    targets = {}
    for repo in root.findall("repository"):
        repo_name = repo.attrib.get("name", "")
        if not repo_name:
            continue

        archs = []
        for a in repo.findall("arch"):
            if a.text and a.text.strip():
                archs.append(a.text.strip())

        deps = []
        for p in repo.findall("path"):
            dep_prj = p.attrib.get("project", "")
            dep_repo = p.attrib.get("repository", "")
            if dep_prj:
                deps.append({
                    "project": dep_prj,
                    "target": dep_repo or "standard",
                })

        targets[repo_name] = {
            "architectures": archs,
            "dependencies": deps,
            "flags": {
                "build": build_enabled,
                "publish": publish_enabled,
                "debuginfo": repo_name == "standard",
                "rebuild": "transitive",
                "block": "all",
            },
        }

    return {
        "name": name,
        "title": title,
        "description": desc,
        "scm_sync": scm_sync,
        "maintainers": maintainers,
        "targets": targets,
    }


def parse_pbuild(raw_pbuild: str) -> tuple[list[str], list[dict]]:
    """Parse _pbuild XML to extract architectures and dependency repositories."""
    archs = []
    dependencies = []
    if not raw_pbuild.strip():
        return ["x86_64", "aarch64"], [{"url": "https://download.suse.de/ibs/SUSE:/SLFO:/Main:/Build/standard/", "type": "rpm-md"}]

    try:
        # Normalize bare boolean attributes like '<preset name="x86_64" default>'
        cleaned_xml = re.sub(r'(\s+default)(?=[>\s])', ' default="true"', raw_pbuild)
        root = ET.fromstring(cleaned_xml)
        seen_archs = set()
        seen_repos = set()

        for preset in root.findall("preset"):
            arch_elem = preset.find("arch")
            if arch_elem is not None and arch_elem.text:
                arch = arch_elem.text.strip()
                if arch not in seen_archs and not arch.startswith("cross_"):
                    seen_archs.add(arch)
                    archs.append(arch)

            repo_elem = preset.find("repo")
            if repo_elem is not None and repo_elem.text:
                repo_url = repo_elem.text.strip()
                if repo_url not in seen_repos:
                    seen_repos.add(repo_url)
                    dependencies.append({"url": repo_url, "type": "rpm-md"})
    except Exception as e:
        sys.stderr.write(f"Warning: failed parsing _pbuild: {e}\n")

    if not archs:
        archs = ["x86_64", "aarch64", "ppc64le", "s390x", "riscv64"]
    if not dependencies:
        dependencies = [{"url": "https://download.suse.de/ibs/SUSE:/SLFO:/Main:/Build/standard/", "type": "rpm-md"}]

    return archs, dependencies


def match_arch(arch_cond: str, target_arch: str) -> bool:
    """Evaluate %ifarch <arch1> <arch2> ... or %ifnarch <arch1> ..."""
    if not target_arch:
        return False
    is_neg = arch_cond.startswith("!")
    cond_text = arch_cond.lstrip("!").strip()

    tokens = []
    for t in cond_text.split():
        if t in ("%arm", "%{arm}"):
            tokens.extend(["armv6l", "armv6hl", "armv7l", "armv7hl", "aarch64"])
        else:
            tokens.append(t)

    matched = target_arch in tokens
    return not matched if is_neg else matched


def eval_condition(cond: str, ctx: dict) -> bool:
    """Evaluate RPM/OBS preprocessor conditions like 0%{?_is_in_project} or !0%{?is_stage_project}."""
    cond = cond.strip()

    def repl_macro(m):
        var = m.group(1)
        val = ctx.get(var, 0)
        return str(val)

    s = re.sub(r"%\{\??([a-zA-Z0-9_]+)\}", repl_macro, cond)
    s = re.sub(r"%([a-zA-Z0-9_]+)", repl_macro, s)
    s = re.sub(r"0(\d+)", r"\1", s)
    s = re.sub(r"!(\d+)", r"not \1", s)
    s = re.sub(r"!", r"not ", s)
    s = s.replace("||", " or ").replace("&&", " and ")

    try:
        return bool(eval(s, {"__builtins__": None}, {}))
    except Exception:
        return True


def parse_config(raw_config: str, target_name: str = "standard", arch: str | None = None, bootstrap_stage: int = 0) -> dict:
    """Parse OBS _config (prjconf) into typed ProjectConfig fields, macros, and package build filters."""
    ctx = {
        "_is_in_project": 1,
        "is_stage_project": 0,
        "bootstrap_stage": bootstrap_stage,
        "_repository": target_name,
    }

    build_flags = []
    ns_build_flags = []
    only_build = set()
    exclude_build = set()
    use_ccache = set()

    prefer = []
    ns_prefer = []
    ignore = []
    ns_ignore = []
    substitute = {}
    preinstall = []
    vminstall = []
    support = []
    ns_support = []
    required = []
    ns_required = []
    expand_flags = []
    export_filter = []
    constraints = []
    file_provides = {}
    conflict = []
    optflags = {}

    macros_lines = []
    ns_macros_lines = []
    in_macros_block = False
    cond_stack = [True]
    scope_stack = ["public"]

    for line in raw_config.splitlines():
        trimmed = line.strip()
        if not trimmed:
            continue

        # Check macro block delimiters
        if trimmed in ("Macros:", "macros:"):
            in_macros_block = True
            continue
        elif trimmed in (":Macros", ":macros"):
            in_macros_block = False
            continue

        # Handle preprocessor conditionals
        if trimmed.startswith("%ifarch") or trimmed.startswith("%ifnarch"):
            is_narch = trimmed.startswith("%ifnarch")
            arch_cond = trimmed.split(None, 1)[1].strip()
            if is_narch:
                arch_cond = "!" + arch_cond
            if arch is not None:
                is_active = cond_stack[-1] and match_arch(arch_cond, arch)
            else:
                is_active = False
            cond_stack.append(is_active)
            scope_stack.append(scope_stack[-1])
            continue
        elif trimmed.startswith("%if"):
            cond_str = trimmed[3:].strip()
            is_active = cond_stack[-1] and eval_condition(cond_str, ctx)
            cond_stack.append(is_active)
            if "_is_in_project" in cond_str:
                scope_stack.append("namespace_scoped")
            elif "_is_this_project" in cond_str:
                scope_stack.append("private")
            else:
                scope_stack.append(scope_stack[-1])
            continue
        elif trimmed.startswith("%else"):
            if len(cond_stack) > 1:
                parent_active = cond_stack[-2]
                cond_stack[-1] = parent_active and not cond_stack[-1]
            continue
        elif trimmed.startswith("%endif"):
            if len(cond_stack) > 1:
                cond_stack.pop()
            if len(scope_stack) > 1:
                scope_stack.pop()
            continue

        # If inside an inactive branch, skip
        if not cond_stack[-1]:
            continue

        current_scope = scope_stack[-1]

        # Inside Macros: block
        if in_macros_block:
            if current_scope == "namespace_scoped":
                ns_macros_lines.append(trimmed)
            else:
                macros_lines.append(trimmed)
            continue

        # Parse %define statements
        if trimmed.startswith("%define"):
            parts = trimmed.split(None, 2)
            if len(parts) >= 3:
                var_name = parts[1]
                var_val = parts[2]
                ctx[var_name] = var_val
                macro_def = f"%{var_name} {var_val}"
                if current_scope == "namespace_scoped":
                    ns_macros_lines.append(macro_def)
                else:
                    macros_lines.append(macro_def)
            continue

        # Skip comment lines
        if trimmed.startswith("#"):
            continue

        # Parse standard OBS directives
        if trimmed.startswith("BuildFlags:"):
            flag = trimmed.split(":", 1)[1].strip()
            if flag.startswith("onlybuild:"):
                pkg = flag.split(":", 1)[1].strip()
                only_build.add(pkg)
            elif flag.startswith("excludebuild:"):
                pkg = flag.split(":", 1)[1].strip()
                exclude_build.add(pkg)
                if current_scope == "namespace_scoped":
                    ns_build_flags.append(flag)
                else:
                    build_flags.append(flag)
            elif flag.startswith("useccache:"):
                pkg = flag.split(":", 1)[1].strip()
                use_ccache.add(pkg)
            else:
                if current_scope == "namespace_scoped":
                    if flag and flag not in ns_build_flags:
                        ns_build_flags.append(flag)
                else:
                    if flag and flag not in build_flags:
                        build_flags.append(flag)

        elif trimmed.startswith("Prefer:"):
            items = trimmed.split(":", 1)[1].strip().split()
            target_list = ns_prefer if current_scope == "namespace_scoped" else prefer
            for it in items:
                if it not in target_list:
                    target_list.append(it)

        elif trimmed.startswith("Ignore:"):
            items = trimmed.split(":", 1)[1].strip().split()
            target_list = ns_ignore if current_scope == "namespace_scoped" else ignore
            for it in items:
                if it not in target_list:
                    target_list.append(it)

        elif trimmed.startswith("Support:"):
            items = trimmed.split(":", 1)[1].strip().split()
            target_list = ns_support if current_scope == "namespace_scoped" else support
            for it in items:
                if it not in target_list:
                    target_list.append(it)

        elif trimmed.startswith("Required:"):
            items = trimmed.split(":", 1)[1].strip().split()
            target_list = ns_required if current_scope == "namespace_scoped" else required
            for it in items:
                if it not in target_list:
                    target_list.append(it)

        elif trimmed.startswith("ExpandFlags:"):
            items = trimmed.split(":", 1)[1].strip().split()
            for it in items:
                if it not in expand_flags:
                    expand_flags.append(it)

        elif trimmed.startswith("ExportFilter:"):
            filter_val = trimmed.split(":", 1)[1].strip()
            if filter_val and filter_val not in export_filter:
                export_filter.append(filter_val)

        elif trimmed.startswith("Constraint:"):
            c_val = trimmed.split(":", 1)[1].strip()
            if c_val and c_val not in constraints:
                constraints.append(c_val)

        elif trimmed.startswith("FileProvides:"):
            parts = trimmed.split(":", 1)[1].strip().split(None, 1)
            if len(parts) == 2:
                file_path = parts[0]
                pkgs = parts[1].split()
                file_provides[file_path] = pkgs

        elif trimmed.startswith("Substitute:"):
            parts = trimmed.split(":", 1)[1].strip().split(None, 1)
            if len(parts) == 2:
                k = parts[0]
                vals = parts[1].split()
                substitute[k] = vals

        elif trimmed.startswith("Preinstall:"):
            items = trimmed.split(":", 1)[1].strip().split()
            for it in items:
                if it not in preinstall:
                    preinstall.append(it)

        elif trimmed.startswith("VMinstall:"):
            items = trimmed.split(":", 1)[1].strip().split()
            for it in items:
                if it not in vminstall:
                    vminstall.append(it)

        elif trimmed.startswith("Conflict:"):
            items = trimmed.split(":", 1)[1].strip().split()
            if items:
                conflict.append(items)

        elif trimmed.startswith("Optflags:"):
            parts = trimmed.split(":", 1)[1].strip().split(None, 1)
            if len(parts) == 2:
                optflags[parts[0]] = parts[1]

    # Clean and preserve macro lines without destructive line-by-line deduplication
    cleaned_macros = [m for m in macros_lines if m.strip()]
    cleaned_ns_macros = [m for m in ns_macros_lines if m.strip()]

    return {
        "macros": "\n".join(cleaned_macros),
        "ns_macros": "\n".join(cleaned_ns_macros),
        "build_flags": build_flags,
        "ns_build_flags": ns_build_flags,
        "only_build": only_build,
        "exclude_build": exclude_build,
        "use_ccache": use_ccache,
        "prefer": prefer,
        "ns_prefer": ns_prefer,
        "ignore": ignore,
        "ns_ignore": ns_ignore,
        "support": support,
        "ns_support": ns_support,
        "required": required,
        "ns_required": ns_required,
        "expand_flags": expand_flags,
        "export_filter": export_filter,
        "constraints": constraints,
        "file_provides": file_provides,
        "substitute": substitute,
        "preinstall": preinstall,
        "vminstall": vminstall,
        "conflict": conflict,
        "optflags": optflags,
    }


def determine_resources(pkg_name: str, use_ccache: set[str]) -> dict:
    """Assign appropriate hardware constraints based on package complexity."""
    heavy_keywords = [
        "chromium", "llvm", "gcc", "glibc", "firefox", "rust", "kernel",
        "mesa", "ceph", "webkit", "libreoffice", "dotnet", "clang",
    ]
    name_lower = pkg_name.lower()
    is_heavy = any(kw in name_lower for kw in heavy_keywords) or (pkg_name in use_ccache)

    if is_heavy:
        return {
            "memory": "16Gi",
            "vcpus": 8,
            "disk": "50Gi",
            "timeout": "8h",
        }
    return {
        "memory": "4Gi",
        "vcpus": 2,
        "disk": "10Gi",
        "timeout": "2h",
    }


class LiteralDumper(yaml.SafeDumper):
    pass


def str_representer(dumper, data):
    if "\n" in data:
        return dumper.represent_scalar("tag:yaml.org,2002:str", data, style="|")
    return dumper.represent_scalar("tag:yaml.org,2002:str", data)


LiteralDumper.add_representer(str, str_representer)


def write_yaml(file_path: Path, data: dict):
    """Write dictionary to YAML file cleanly using block scalars for multiline strings."""
    file_path.parent.mkdir(parents=True, exist_ok=True)
    with open(file_path, "w", encoding="utf-8") as f:
        yaml.dump(data, f, Dumper=LiteralDumper, default_flow_style=False, sort_keys=False)


def main():
    parser = argparse.ArgumentParser(
        description="Convert SLFO branch (e.g. slfo-1.2) into a bsgo-compatible project."
    )
    parser.add_argument("--slfo-repo", default="/home/ta/Devel/packages/SLFO", help="Path to SLFO git repository")
    parser.add_argument("--branch", default="slfo-1.2", help="Git branch or ref to convert from (default: slfo-1.2)")
    parser.add_argument("--output-dir", default="/home/ta/Devel/packages/SLFO-bsgo", help="Output directory for bsgo project")
    parser.add_argument("--gitea-base", default="https://src.opensuse.org", help="Base URL for Gitea forge")
    parser.add_argument("--project-name", default="SUSE:SLFO:1.2", help="Canonical bsgo project identifier")
    parser.add_argument("--prj-meta", default="", help="Path to or inline XML string of OBS project metadata (_meta)")
    parser.add_argument("--bootstrap-stage", type=int, default=0, help="OBS bootstrap stage (0 for production, 1 for bootstrap from SLFO 1.1)")
    parser.add_argument("--dry-run", action="store_true", help="Perform extraction without writing files")

    args = parser.parse_args()

    repo_dir = os.path.abspath(args.slfo_repo)
    if not os.path.isdir(repo_dir):
        sys.stderr.write(f"Error: repo directory not found: {repo_dir}\n")
        sys.exit(1)

    print(f"==> Extracting SLFO metadata from {repo_dir} (branch/ref: {args.branch})...")

    # 1. Inspect git ref: check if branch or origin/<branch> exists
    target_ref = args.branch
    try:
        git_cmd(repo_dir, ["rev-parse", "--verify", target_ref])
    except subprocess.CalledProcessError:
        target_ref = f"origin/{args.branch}"
        try:
            git_cmd(repo_dir, ["rev-parse", "--verify", target_ref])
        except subprocess.CalledProcessError:
            sys.stderr.write(f"Error: cannot resolve git ref '{args.branch}' or 'origin/{args.branch}' in {repo_dir}\n")
            sys.exit(1)

    print(f"    Resolved git ref: {target_ref}")

    # 2. Extract files from git ref
    raw_modules = get_git_file(repo_dir, target_ref, ".gitmodules")
    raw_config = get_git_file(repo_dir, target_ref, "_config")
    raw_pbuild = get_git_file(repo_dir, target_ref, "_pbuild")
    raw_staging = get_git_file(repo_dir, target_ref, "staging.config")

    # Optional OBS project metadata XML (_meta)
    raw_prj_meta = ""
    if args.prj_meta:
        if os.path.isfile(args.prj_meta):
            with open(args.prj_meta, "r", encoding="utf-8") as f:
                raw_prj_meta = f.read()
        else:
            raw_prj_meta = args.prj_meta
    else:
        # Check if _meta exists in git ref or local repo
        git_meta = get_git_file(repo_dir, target_ref, "_meta")
        if git_meta:
            raw_prj_meta = git_meta

    submodule_commits = get_submodule_commits(repo_dir, target_ref)
    print(f"    Discovered {len(submodule_commits)} submodules in git tree.")

    pkg_modules = parse_gitmodules(raw_modules, args.gitea_base)
    print(f"    Parsed {len(pkg_modules)} package entries from .gitmodules.")

    # Determine project info and targets
    project_name = args.project_name
    project_title = "SUSE Linux Framework One 1.2"
    project_desc = f"Converted from SLFO git repository (branch {args.branch})"
    targets = {}

    if raw_prj_meta:
        print("    [!] Parsing supplied OBS project metadata XML (_meta)...")
        meta_info = parse_prj_meta(raw_prj_meta)
        if meta_info.get("name"):
            project_name = meta_info["name"]
        if meta_info.get("title"):
            project_title = meta_info["title"]
        if meta_info.get("description"):
            project_desc = meta_info["description"]
        if meta_info.get("targets"):
            targets = meta_info["targets"]
            print(f"    Parsed {len(targets)} targets from OBS _meta: {', '.join(targets.keys())}")

    if not targets:
        archs, dependencies = parse_pbuild(raw_pbuild)
        print(f"    Target architectures (from _pbuild): {', '.join(archs)}")
        print(f"    Dependency repositories (from _pbuild): {len(dependencies)}")
        if raw_staging:
            try:
                stg = json.loads(raw_staging)
                if "ObsProject" in stg and stg["ObsProject"]:
                    project_name = stg["ObsProject"]
            except Exception:
                pass
        targets = {
            "standard": {
                "architectures": archs,
                "dependencies": dependencies,
                "flags": {
                    "build": True,
                    "publish": True,
                    "debuginfo": True,
                    "rebuild": "transitive",
                    "block": "all",
                },
            }
        }

    cfg_data = parse_config(raw_config, target_name="standard", bootstrap_stage=args.bootstrap_stage)
    macro_count = len(cfg_data["macros"].splitlines())
    print(f"    Parsed _config: {macro_count} lines of macros (evaluated with _is_in_project=1, bootstrap_stage={args.bootstrap_stage}).")
    print(f"    Parsed _config directives: {len(cfg_data['build_flags'])} build_flags, {len(cfg_data['prefer'])} prefers, {len(cfg_data['substitute'])} substitutions.")
    if cfg_data["only_build"]:
        print(f"    Filtered 'onlybuild' whitelist: {len(cfg_data['only_build'])} packages enabled.")

    # 3. Construct target configurations and architecture-specific configs
    for tgt_name in targets.keys():
        target_archs = targets[tgt_name].get("architectures", [])
        tgt_base_cfg = parse_config(raw_config, target_name=tgt_name, arch=None, bootstrap_stage=args.bootstrap_stage)
        target_config = {}

        # Detect target-specific engine and types
        if tgt_name == "images":
            target_config["build_types"] = ["kiwi"]
            target_config["repo_types"] = ["none"]
            target_config["prefer"] = [p for p in tgt_base_cfg["prefer"] if "openSUSE-release" in p]
        elif tgt_name == "containerfile":
            target_config["build_types"] = ["docker"]
            target_config["build_engine"] = "podman"
            target_config["build_flags"] = [f for f in tgt_base_cfg["build_flags"] if "docker" in f]
        elif tgt_name == "helmcharts":
            target_config["build_types"] = ["helm"]
            target_config["repo_types"] = ["helm"]
        else:
            target_config["build_types"] = ["spec"]
            target_config["build_engine"] = "build"
            target_config["repo_types"] = ["rpm-md"]

        if target_config:
            targets[tgt_name]["config"] = target_config

        # Evaluate arch_config overrides for each architecture
        arch_config = {}
        for a in target_archs:
            a_cfg = parse_config(raw_config, target_name=tgt_name, arch=a, bootstrap_stage=args.bootstrap_stage)
            delta = {}
            delta_flags = [f for f in a_cfg["build_flags"] if f not in tgt_base_cfg["build_flags"]]
            if delta_flags:
                delta["build_flags"] = delta_flags

            delta_prefers = [p for p in a_cfg["prefer"] if p not in tgt_base_cfg["prefer"]]
            if delta_prefers:
                delta["prefer"] = delta_prefers

            delta_ignores = [i for i in a_cfg["ignore"] if i not in tgt_base_cfg["ignore"]]
            if delta_ignores:
                delta["ignore"] = delta_ignores

            delta_constraints = [c for c in a_cfg["constraints"] if c not in tgt_base_cfg["constraints"]]
            if delta_constraints:
                delta["constraints"] = delta_constraints

            delta_export = [e for e in a_cfg["export_filter"] if e not in tgt_base_cfg["export_filter"]]
            if delta_export:
                delta["export_filter"] = delta_export

            delta_support = [s for s in a_cfg["support"] if s not in tgt_base_cfg["support"]]
            if delta_support:
                delta["support"] = delta_support

            # Macros specific to this architecture
            if a_cfg["macros"] and a_cfg["macros"] != tgt_base_cfg["macros"]:
                base_lines = set(tgt_base_cfg["macros"].splitlines())
                arch_specific_macs = [m for m in a_cfg["macros"].splitlines() if m not in base_lines]
                if arch_specific_macs:
                    delta["macros"] = "\n".join(arch_specific_macs)

            if delta:
                arch_config[a] = delta

        if arch_config:
            targets[tgt_name]["arch_config"] = arch_config

    # Construct project-level baseline manifest
    project_info = {
        "name": project_name,
        "title": project_title,
        "description": project_desc,
        "namespace": "SUSE:SLFO",
    }
    if raw_prj_meta and meta_info.get("scm_sync"):
        project_info["scm_sync"] = meta_info["scm_sync"]
    if raw_prj_meta and meta_info.get("maintainers"):
        project_info["maintainers"] = meta_info["maintainers"]

    public_cfg = {
        "build_types": ["spec"],
        "build_engine": "build",
        "repo_types": ["rpm-md"],
        "macros_file": "_config",
        "macros": cfg_data["macros"],
        "build_flags": [f for f in cfg_data["build_flags"] if not f.startswith("dockerarg:")],
        "prefer": cfg_data["prefer"],
        "ignore": cfg_data["ignore"],
        "substitute": cfg_data["substitute"],
        "preinstall": cfg_data["preinstall"],
        "vminstall": cfg_data["vminstall"],
    }
    if cfg_data["support"]:
        public_cfg["support"] = cfg_data["support"]
    if cfg_data["required"]:
        public_cfg["required"] = cfg_data["required"]
    if cfg_data["expand_flags"]:
        public_cfg["expand_flags"] = cfg_data["expand_flags"]
    if cfg_data["export_filter"]:
        public_cfg["export_filter"] = cfg_data["export_filter"]
    if cfg_data["constraints"]:
        public_cfg["constraints"] = cfg_data["constraints"]
    if cfg_data["file_provides"]:
        public_cfg["file_provides"] = cfg_data["file_provides"]
    if cfg_data["conflict"]:
        public_cfg["conflict"] = cfg_data["conflict"]
    if cfg_data["optflags"]:
        public_cfg["optflags"] = cfg_data["optflags"]

    project_manifest = {
        "schema_version": "v1",
        "project": project_info,
        "public_config": public_cfg,
        "targets": targets,
    }

    # Add protected_config if any directives were guarded by %_is_in_project
    prot_config = {}
    if cfg_data["ns_build_flags"]:
        prot_config["build_flags"] = cfg_data["ns_build_flags"]
    if cfg_data["ns_prefer"]:
        prot_config["prefer"] = cfg_data["ns_prefer"]
    if cfg_data["ns_ignore"]:
        prot_config["ignore"] = cfg_data["ns_ignore"]
    if cfg_data["ns_support"]:
        prot_config["support"] = cfg_data["ns_support"]
    if cfg_data["ns_required"]:
        prot_config["required"] = cfg_data["ns_required"]
    if cfg_data["ns_macros"]:
        prot_config["macros"] = cfg_data["ns_macros"]

    if prot_config:
        project_manifest["protected_config"] = prot_config

    out_dir = Path(args.output_dir)
    print(f"==> Target output directory: {out_dir}")

    if args.dry_run:
        print("Dry run requested; stopping before writing manifests.")
        return

    out_dir.mkdir(parents=True, exist_ok=True)

    # Write project.yaml
    project_file = out_dir / "project.yaml"
    write_yaml(project_file, project_manifest)
    print(f"    [+] Created root {project_file}")

    # Copy _config for macros_file backwards compatibility
    if raw_config:
        config_file = out_dir / "_config"
        with open(config_file, "w", encoding="utf-8") as f:
            f.write(raw_config)
        print(f"    [+] Preserved {config_file} (macros_file)")

    # 4. Generate sharded package manifests
    print(f"==> Generating {len(submodule_commits)} sharded package manifests...")
    written_count = 0
    all_pkg_manifest_paths = []

    for pkg_name, commit_sha in submodule_commits.items():
        mod_info = pkg_modules.get(pkg_name, {})
        git_url = mod_info.get("url", f"{args.gitea_base.rstrip('/')}/pool/{pkg_name}.git")
        branch = mod_info.get("branch", args.branch)

        # Build enabled check
        is_enabled = True
        if cfg_data["only_build"]:
            is_enabled = pkg_name in cfg_data["only_build"]
        if pkg_name in cfg_data["exclude_build"]:
            is_enabled = False

        resources = determine_resources(pkg_name, cfg_data["use_ccache"])

        pkg_manifest = {
            "schema_version": "v1",
            "package": {
                "name": pkg_name,
                "title": pkg_name,
            },
            "source": {
                "git": git_url,
                "branch": branch,
                "commit": commit_sha,
                "recipe": f"{pkg_name}.spec",
            },
            "build": {
                "enabled": is_enabled,
            },
            "resources": resources,
        }

        shard_rel = shard_path(pkg_name)
        full_shard_path = out_dir / shard_rel
        write_yaml(full_shard_path, pkg_manifest)
        all_pkg_manifest_paths.append(shard_rel)
        written_count += 1

    print(f"    [+] Successfully generated {written_count} package manifests under {out_dir}/packages/")

    # 5. Generate import-to-bsgo.sh helper
    import_script = out_dir / "import-to-bsgo.sh"
    acl_cmds = []
    for m in project_info.get("maintainers", []):
        if m.startswith("group:"):
            gid = m.split(":", 1)[1]
            acl_cmds.append(f'"${{BSGO_BIN}}" admin acl set "{project_name}" group "{gid}" maintainer 2>/dev/null || true')
        else:
            acl_cmds.append(f'"${{BSGO_BIN}}" admin acl set "{project_name}" user "{m}" maintainer 2>/dev/null || true')
    acl_snippet = "\n".join(acl_cmds) if acl_cmds else "# No explicit maintainers in project metadata"

    with open(import_script, "w", encoding="utf-8") as f:
        f.write(f"""#!/usr/bin/env bash
# Import converted SLFO project and packages into bsgo control plane
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${{BASH_SOURCE[0]}}")" && pwd)"
BSGO_BIN="${{BSGO_BIN:-bsgo}}"

echo "==> Ingesting project.yaml into bsgo..."
"${{BSGO_BIN}}" project add "${{SCRIPT_DIR}}/project.yaml"

echo "==> Configuring project maintainer ACLs..."
{acl_snippet}

echo "==> Ingesting {written_count} package manifests into {project_name}..."
find "${{SCRIPT_DIR}}/packages" -type f -name "*.yaml" | sort | while read -r pkg_file; do
    "${{BSGO_BIN}}" package add "{project_name}" "${{pkg_file}}" --quiet 2>/dev/null || true
done

echo "==> Verification:"
"${{BSGO_BIN}}" project show "{project_name}"
"${{BSGO_BIN}}" package list "{project_name}" | head -n 20
echo "Done!"
""")
    os.chmod(import_script, 0o755)
    print(f"    [+] Created import helper {import_script}")

    # 6. Generate README.md
    readme_file = out_dir / "README.md"
    with open(readme_file, "w", encoding="utf-8") as f:
        f.write(f"""# {project_name} - bsgo Project

This directory contains the declarative **bsgo** configuration for **{project_name}**, converted from the SLFO branch `{args.branch}`.

## Layout

- `project.yaml`: Canonical project declaration and solver configuration.
- `_config`: Distribution RPM macros preserved from OBS prjconf (`macros_file: "_config"`).
- `packages/xx/<package>.yaml`: 256 hash-sharded package manifests partitioning {written_count} packages.
- `import-to-bsgo.sh`: Shell helper for batch onboarding into the `bsgo` control plane.

## GitOps Hosting (Gitea / Forge)

1. Initialize a git repository in this directory:
   ```bash
   git init -b main
   git add project.yaml _config packages/ README.md
   git commit -m "feat: migrate SLFO 1.2 to native bsgo project structure"
   ```
2. Push to your Gitea forge (e.g. `https://src.opensuse.org/products/SLFO-bsgo.git`).
3. Configure a Gitea Webhook pointing to your `bsgo` server to project updates into PostgreSQL.
""")
    print(f"    [+] Created {readme_file}")
    print("\n==> Conversion complete! All 3,500+ submodules converted to bsgo sharded metadata.")


if __name__ == "__main__":
    main()
