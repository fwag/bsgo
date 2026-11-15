#define _GNU_SOURCE
#include "c_bridge.h"
#include <solv/poolarch.h>
#include <solv/repo.h>
#include <solv/evr.h>
#include <solv/pool_parserpmrichdep.h>
#include <solv/testcase.h>
#include <solv/repodata.h>
#include <solv/dataiterator.h>

Pool *bsgo_pool_create(const char *arch) {
    Pool *pool = pool_create();
    if (arch && *arch) {
        pool_setarch(pool, arch);
    }
    return pool;
}

void bsgo_pool_free(Pool *pool) {
    if (pool) {
        if (pool->considered) {
            map_free(pool->considered);
            free(pool->considered);
            pool->considered = NULL;
        }
        pool_free(pool);
    }
}

Repo *bsgo_repo_create(Pool *pool, const char *name) {
    return repo_create(pool, name);
}

Repo *bsgo_load_repo_from_mem(Pool *pool, const char *name, const void *buf, size_t len) {
    if (!pool || !buf || len == 0) return NULL;

    FILE *fp = fmemopen((void*)buf, len, "r");
    if (!fp) return NULL;

    Repo *repo = repo_create(pool, name);
    if (repo_add_solv(repo, fp, 0) != 0) {
        fclose(fp);
        repo_free(repo, 1);
        return NULL;
    }
    fclose(fp);
    return repo;
}

Repo *bsgo_load_repo_from_rpmmd_mem(Pool *pool, const char *name, const void *buf, size_t len) {
    if (!pool || !buf || len == 0) return NULL;

    FILE *fp = fmemopen((void*)buf, len, "r");
    if (!fp) return NULL;

    Repo *repo = repo_create(pool, name);
    if (repo_add_rpmmd(repo, fp, NULL, 0) != 0) {
        fclose(fp);
        repo_free(repo, 1);
        return NULL;
    }
    fclose(fp);

    return repo;
}

int bsgo_dump_repo_to_mem(Repo *repo, void **out_buf, size_t *out_len) {
    if (!repo || !out_buf || !out_len) return -1;

    char *buf = NULL;
    size_t size = 0;
    FILE *fp = open_memstream(&buf, &size);
    if (!fp) return -1;

    if (repo_write(repo, fp) != 0) {
        fclose(fp);
        free(buf);
        return -1;
    }
    fclose(fp);

    *out_buf = buf;
    *out_len = size;
    return 0;
}

int bsgo_add_package(Repo *repo, const char *name, const char *evr, const char *arch,
                     int num_provides, const char **provides,
                     int num_requires, const char **requires) {
    if (!repo || !name || !evr) return -1;
    Pool *pool = repo->pool;

    Id s_id = repo_add_solvable(repo);
    Solvable *s = pool->solvables + s_id;
    s->name = pool_str2id(pool, name, 1);
    s->evr = pool_str2id(pool, evr, 1);
    s->arch = arch ? pool_str2id(pool, arch, 1) : ARCH_NOARCH;

    s->provides = repo_addid_dep(repo, s->provides, pool_rel2id(pool, s->name, s->evr, REL_EQ, 1), 0);
    for (int i = 0; i < num_provides; i++) {
        Id prov_id = bsgo_str2dep(pool, provides[i]);
        s->provides = repo_addid_dep(repo, s->provides, prov_id, 0);
    }

    for (int i = 0; i < num_requires; i++) {
        Id req_id = bsgo_str2dep(pool, requires[i]);
        s->requires = repo_addid_dep(repo, s->requires, req_id, 0);
    }

    return 0;
}


Id bsgo_repo_add_solvable(Repo *repo, const char *name, const char *evr, const char *arch) {
    if (!repo || !name || !evr || !arch) return 0;
    Pool *pool = repo->pool;
    Id sid = repo_add_solvable(repo);
    Solvable *s = pool->solvables + sid;
    s->name = pool_str2id(pool, name, 1);
    s->evr = pool_str2id(pool, evr, 1);
    s->arch = pool_str2id(pool, arch, 1);
    return sid;
}

int bsgo_solvable_add_dep(Repo *repo, Id sid, int kind, const char *dep, int pre) {
    if (!repo || sid <= 0 || !dep) return -1;
    Pool *pool = repo->pool;
    Id id = bsgo_str2dep(pool, dep);
    if (!id) return -1;
    Solvable *s = pool->solvables + sid;
    switch (kind) {
    case BSGO_DEP_PROVIDES:    s->provides = repo_addid_dep(repo, s->provides, id, 0); break;
    case BSGO_DEP_REQUIRES:
        s->requires = repo_addid_dep(repo, s->requires, id, pre ? -SOLVABLE_PREREQMARKER : SOLVABLE_PREREQMARKER);
        break;
    case BSGO_DEP_CONFLICTS:   s->conflicts = repo_addid_dep(repo, s->conflicts, id, 0); break;
    case BSGO_DEP_OBSOLETES:   s->obsoletes = repo_addid_dep(repo, s->obsoletes, id, 0); break;
    case BSGO_DEP_RECOMMENDS:  s->recommends = repo_addid_dep(repo, s->recommends, id, 0); break;
    case BSGO_DEP_SUGGESTS:    s->suggests = repo_addid_dep(repo, s->suggests, id, 0); break;
    case BSGO_DEP_SUPPLEMENTS: s->supplements = repo_addid_dep(repo, s->supplements, id, 0); break;
    case BSGO_DEP_ENHANCES:    s->enhances = repo_addid_dep(repo, s->enhances, id, 0); break;
    default: return -1;
    }
    return 0;
}

/* Files are kept in the repodata file list, exactly where libsolv's rpm-md
 * parser keeps the <file> entries of primary.xml. */
int bsgo_solvable_add_file(Repo *repo, Id sid, const char *path) {
    if (!repo || sid <= 0 || !path || *path != '/') return -1;
    const char *slash = strrchr(path, '/');
    char *dir = strndup(path, slash == path ? 1 : (size_t)(slash - path));
    if (!dir) return -1;
    Repodata *data = repo_last_repodata(repo);
    Id dirid = repodata_str2dir(data, dir, 1);
    repodata_add_dirstr(data, sid, SOLVABLE_FILELIST, dirid, slash + 1);
    free(dir);
    return 0;
}

int bsgo_solvable_set_sha256(Repo *repo, Id sid, const char *hex) {
    if (!repo || sid <= 0 || !hex) return -1;
    Repodata *data = repo_last_repodata(repo);
    if (!data) return -1;
    repodata_set_checksum(data, sid, SOLVABLE_CHECKSUM, REPOKEY_TYPE_SHA256, hex);
    return 0;
}

int bsgo_repo_finish(Repo *repo) {
    if (!repo) return -1;
    repo_internalize(repo);
    return 0;
}

Id bsgo_str2dep(Pool *pool, const char *s) {
    if (!s || !*s) return 0;
    if (*s == '(') return pool_parserpmrichdep(pool, s);
    return testcase_str2dep(pool, s);
}

/* Rank of an architecture for same-name selection: lower is better, noarch is worst
 * (OBS archpath order is "x86_64:i686:i586:noarch"). */
static unsigned int arch_rank(Pool *pool, Id arch) {
    if (arch == ARCH_NOARCH) return 1u << 20;
    unsigned int score = pool->id2arch ? pool_arch2score(pool, arch) : 0;
    return score ? score : (1u << 19);
}

static int better_solvable(Pool *pool, Solvable *a, Solvable *b) {
    unsigned int ra = arch_rank(pool, a->arch);
    unsigned int rb = arch_rank(pool, b->arch);
    if (ra != rb) return ra < rb;
    return pool_evrcmp(pool, a->evr, b->evr, EVRCMP_COMPARE) > 0;
}

int bsgo_pool_prepare(Pool *pool) {
    if (!pool) return -1;
    pool_freewhatprovides(pool);
    if (pool->considered) {
        map_free(pool->considered);
        free(pool->considered);
        pool->considered = NULL;
    }
    int nstr = pool->ss.nstrings;
    Id *best = calloc(nstr + 1, sizeof(Id));
    Id *owner = calloc(nstr + 1, sizeof(Id));
    Map *considered = malloc(sizeof(Map));
    if (!best || !owner || !considered) {
        free(best);
        free(owner);
        free(considered);
        return -1;
    }
    map_init(considered, pool->nsolvables);

    Repo *r;
    int ri;
    FOR_REPOS(ri, r) {
        Id p;
        Solvable *s;
        FOR_REPO_SOLVABLES(r, p, s) {
            if (!pool_installable(pool, s)) continue;
            Id n = s->name;
            if (owner[n] && owner[n] != r->repoid) continue;
            owner[n] = r->repoid;
            if (!best[n] || better_solvable(pool, s, pool->solvables + best[n])) best[n] = p;
        }
    }
    for (int n = 0; n < nstr; n++) {
        if (best[n]) MAPSET(considered, best[n]);
    }
    free(best);
    free(owner);
    pool->considered = considered;
    pool_createwhatprovides(pool);
    return 0;
}

int bsgo_whatprovides(Pool *pool, Id dep, Id **out) {
    *out = NULL;
    int n = 0;
    Id p, pp;
    FOR_PROVIDES(p, pp, dep) n++;
    if (n == 0) return 0;
    Id *res = malloc(sizeof(Id) * n);
    if (!res) return -1;
    int i = 0;
    FOR_PROVIDES(p, pp, dep) res[i++] = p;
    *out = res;
    return n;
}

int bsgo_solvable_info(Pool *pool, Id p, BsgoSolvInfo *out) {
    if (!pool || p <= 0 || p >= pool->nsolvables) return -1;
    Solvable *s = pool->solvables + p;
    out->name = pool_id2str(pool, s->name);
    out->evr = pool_id2str(pool, s->evr);
    out->arch = pool_id2str(pool, s->arch);
    out->repo = s->repo ? s->repo->repoid - 1 : -1;
    out->checksum = NULL;
    out->location = NULL;
    out->sha256 = 0;
    if (s->repo && s->repo->nrepodata > 0) {
        Id chktype = 0;
        out->checksum = solvable_lookup_checksum(s, SOLVABLE_CHECKSUM, &chktype);
        out->sha256 = out->checksum && chktype == REPOKEY_TYPE_SHA256;
        unsigned int medianr = 0;
        out->location = solvable_lookup_location(s, &medianr);
    }
    return 0;
}

int bsgo_solvable_deps(Pool *pool, Id p, int which, Id **out) {
    static const Id keys[] = {SOLVABLE_REQUIRES, SOLVABLE_CONFLICTS, SOLVABLE_OBSOLETES,
                              SOLVABLE_RECOMMENDS, SOLVABLE_SUPPLEMENTS, SOLVABLE_PROVIDES};
    *out = NULL;
    if (which < 0 || which >= (int)(sizeof(keys) / sizeof(keys[0]))) return -1;
    Queue q;
    queue_init(&q);
    /* kind 5 (provides) returns only the explicit provides, without the file list
     * entries libsolv keeps behind SOLVABLE_FILEMARKER. */
    Id marker = keys[which] == SOLVABLE_PROVIDES ? -SOLVABLE_FILEMARKER : 0;
    solvable_lookup_deparray(pool->solvables + p, keys[which], &q, marker);
    int n = 0;
    Id *res = q.count > 0 ? malloc(sizeof(Id) * q.count) : NULL;
    if (q.count > 0 && !res) {
        queue_free(&q);
        return -1;
    }
    for (int i = 0; i < q.count; i++) {
        Id d = q.elements[i];
        if (d == SOLVABLE_PREREQMARKER || d == SOLVABLE_FILEMARKER) continue;
        res[n++] = d;
    }
    if (n == 0) {
        free(res);
        res = NULL;
    }
    *out = res;
    queue_free(&q);
    return n;
}

int bsgo_dep_decompose(Pool *pool, Id dep, Id *name, Id *evr, int *flags) {
    if (!ISRELDEP(dep)) return 0;
    Reldep *rd = GETRELDEP(pool, dep);
    *name = rd->name;
    *evr = rd->evr;
    *flags = rd->flags;
    return 1;
}

const char *bsgo_dep2str(Pool *pool, Id dep) {
    return pool_dep2str(pool, dep);
}

const char *bsgo_id2str(Pool *pool, Id id) {
    return pool_id2str(pool, id);
}

/* All provides of every solvable plus the file list entries libsolv keeps in
 * repodata, one "name: dep" per line. */
char *bsgo_all_provides(Pool *pool) {
    char *buf = NULL;
    size_t size = 0;
    FILE *fp = open_memstream(&buf, &size);
    if (!fp) return NULL;
    for (Id p = 2; p < pool->nsolvables; p++) {
        Solvable *s = pool->solvables + p;
        if (!s->repo) continue;
        Queue q;
        queue_init(&q);
        solvable_lookup_deparray(s, SOLVABLE_PROVIDES, &q, 0);
        for (int i = 0; i < q.count; i++) {
            if (q.elements[i] == SOLVABLE_FILEMARKER) continue;
            fprintf(fp, "%s: %s\n", pool_id2str(pool, s->name), pool_dep2str(pool, q.elements[i]));
        }
        queue_free(&q);
        Dataiterator di;
        dataiterator_init(&di, pool, s->repo, p, SOLVABLE_FILELIST, NULL, 0);
        while (dataiterator_step(&di)) {
            fprintf(fp, "%s: %s\n", pool_id2str(pool, s->name), repodata_dir2str(di.data, di.kv.id, di.kv.str));
        }
        dataiterator_free(&di);
    }
    fclose(fp);
    return buf;
}
