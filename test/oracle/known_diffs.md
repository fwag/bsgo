# Known differences between buildconfig.Parse and Build::read_config

Status: zero differences on all fixtures (SUSE_SLFO_1.1, SUSE_SLFO_1.2 and the
hand-made fixtures, for x86_64, aarch64 and i586). The items below are
constructs that are intentionally not identical or not ported; none of them
occurs in the fixtures.

## Intentional differences

- Warnings: obs-build prints "unknown keyword in config" to stderr. The Go
  parser records them in `Config.Warnings` (not part of the compared JSON).
- Parse errors: Perl returns the hash with `parse_error` set; Go additionally
  returns a non-nil `error` carrying the same text (the `Config` is still
  returned).
- Regex dialect: the Lua-pattern subset of `%{gsub:...}` is translated to Go
  RE2 syntax instead of Perl regexes. Error text for an invalid pattern
  differs (the error is still reported as `gsub macro: ...`), and Perl-only
  regex features cannot appear in a translated pattern.
- Number formatting in `%[...]`/`%if` expressions: integers and doubles are
  formatted like Perl for the common cases (integers, `%.15g` otherwise);
  values beyond 2^53 or Perl-specific forms such as `0 but true`, `Inf`, `NaN`
  or hex strings are not reproduced.
- Whitespace classes: field splitting uses the ASCII whitespace set of Perl's
  `\s`. Go's `\s` inside regular expressions excludes vertical tab, which can
  differ from Perl for lines containing `\v`.
- Multi-byte input: Go regexps work on UTF-8 runes while Perl treats the file
  as bytes; only invalid UTF-8 or non-ASCII separators can behave differently.
- `$` before a trailing newline: Perl's `$` also matches before a final `\n`;
  lines are split on `\n` so this only matters for embedded newlines produced
  by multi-line `%define` bodies, where the equivalent `\n?$` is used.
- An empty `Release:` line sets `release` to undef in Perl; Go yields `""`
  (the oracle normalizer maps undef scalars to `""`).

## Not ported (out of scope for WP1a)

- `Build::read_config_dist`, `find_config_file`, `slurp_config_file` and
  `combine_configs` (`#!PrependConfigFile`, `FromScratch` merging across
  files). Layering of configs is done by the later `Layer` work package by
  concatenating text before `Parse`.
- `%include` / `%{load:...}` (only active in spec file parsing with an include
  callback, never for project configs). `%{load:}` reports an "unsupported
  builtin macro" parse error exactly like Perl without a callback.
- Post-parse helpers such as `Build::do_subst`, `expand`, `get_build`.

## Quirks of obs-build that are reproduced on purpose

- `%[ expr ]` extends to the end of the line because `macroend` builds a
  bracket-scan regex that never matches for `[`/`]`; text after the closing
  bracket is dropped and a "syntax error in expression" parse error is set
  (fixture `handmade/bracket_midline.conf`).
- For `v"..."` literals inside `%[ ]` with macro expansion, obs-build drops the
  first two characters of the literal before expanding.
- Only the first line of a multi-line macro expansion is recorded as a
  directive line; the remaining lines are re-parsed for conditionals but not
  interpreted as directives.
- `%if`, `%ifarch`, ... are detected by an unanchored match, anywhere in a
  line.
- `%global` and `%define` bodies are only expanded as described in Rpm.pm;
  `s/%/%%/` for parametric macro arguments only escapes the first `%`.

# Known differences between recipe.Parse and Build::parse (spec recipes)

Corpus: 458 SLFO 1.2 packages (503 specs, 50 `_multibuild`), expanded to 602
units (plain packages plus `pkg:flavor` units), parsed with the SLFO 1.2
prjconf (layered with the getconfig header, worker variant with Macros:
blocks) for x86_64. Compared per unit: name, version, release, epoch, deps
(ordered), subpacks, exclarch, badarch, buildtarget, buildconstraint, error.
Result: 602 of 602 units identical, zero diffs.

Intentional differences and unported parts (none occurs in the corpus):

- `%(...)` and unknown-macro failures are not errors in Build::Rpm::parse for
  specs (only for configs): Perl prints a warning and substitutes the text
  `MACRO`. Go does the same and reports the warning in `Info.Warnings`.
- BuildConflicts are emitted as `-name` like Perl (its `conflictdeps` switch
  defaults to off); `Options.ConflictDeps` selects `!name`.
- `# neededforbuild`, `%include` (include callback), `%description` capture,
  `Requires(pre)` prereqs, remote-asset/createarchive directives and the
  `configdependent` flag are not ported (not scheduler inputs for this
  milestone).
- `bs_srcserver:findfile` is only ported for spec recipes; kiwi, docker and
  the other recipe types return `recipe.ErrUnsupported`.
- Perl's `dep_qualifier_mismatch` is inverted in effect (a dependency
  qualified with the target arch is dropped); the quirk is ported as is.
