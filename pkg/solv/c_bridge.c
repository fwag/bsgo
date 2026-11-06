#define _GNU_SOURCE
#include "c_bridge.h"
#include <solv/poolarch.h>

Pool *bsgo_pool_create(const char *arch) {
    Pool *pool = pool_create();
    if (arch && *arch) {
        pool_setarch(pool, arch);
    }
    return pool;
}

void bsgo_pool_free(Pool *pool) {
    if (pool) {
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
        Id prov_id = pool_str2id(pool, provides[i], 1);
        s->provides = repo_addid_dep(repo, s->provides, prov_id, 0);
    }

    for (int i = 0; i < num_requires; i++) {
        Id req_id = pool_str2id(pool, requires[i], 1);
        s->requires = repo_addid_dep(repo, s->requires, req_id, 0);
    }

    return 0;
}

static Id select_provider(Pool *pool, Queue *plist, int num_prefers, const char **prefers, int *ambiguous) {
    *ambiguous = 0;
    if (plist->count == 0) return 0;
    if (plist->count == 1) return plist->elements[0];

    Id pref_chosen = 0;
    int pref_matches = 0;
    for (int i = 0; i < plist->count; i++) {
        Solvable *s = pool->solvables + plist->elements[i];
        const char *sname = pool_id2str(pool, s->name);
        for (int j = 0; j < num_prefers; j++) {
            if (strcmp(sname, prefers[j]) == 0) {
                pref_chosen = plist->elements[i];
                pref_matches++;
                break;
            }
        }
    }

    if (pref_matches == 1) {
        return pref_chosen;
    }

    *ambiguous = 1;
    return 0;
}

static void toposort_solvables(Pool *pool, Queue *installed, Queue *sorted) {
    Map placed;
    map_init(&placed, pool->nsolvables);

    int remaining = installed->count;
    while (remaining > 0) {
        int progress = 0;
        for (int i = 0; i < installed->count; i++) {
            Id s_id = installed->elements[i];
            if (MAPTST(&placed, s_id)) continue;

            Solvable *s = pool->solvables + s_id;
            int deps_satisfied = 1;

            if (s->requires) {
                Id *rp = s->repo->idarraydata + s->requires;
                while (*rp) {
                    Id req = *rp++;
                    Queue providers;
                    queue_init(&providers);
                    Id p, d;
                    FOR_PROVIDES(p, d, req) {
                        for (int k = 0; k < installed->count; k++) {
                            if (installed->elements[k] == p && !MAPTST(&placed, p)) {
                                deps_satisfied = 0;
                                break;
                            }
                        }
                        if (!deps_satisfied) break;
                    }
                    queue_free(&providers);
                    if (!deps_satisfied) break;
                }
            }

            if (deps_satisfied) {
                MAPSET(&placed, s_id);
                queue_push(sorted, s_id);
                remaining--;
                progress = 1;
            }
        }

        if (!progress) {
            // Cycle fallback: push remaining in current order
            for (int i = 0; i < installed->count; i++) {
                Id s_id = installed->elements[i];
                if (!MAPTST(&placed, s_id)) {
                    queue_push(sorted, s_id);
                }
            }
            break;
        }
    }
    map_free(&placed);
}

ExpansionOutcome bsgo_expand(Pool *pool,
                             int num_reqs, const char **reqs,
                             int num_prefers, const char **prefers) {
    ExpansionOutcome outcome;
    memset(&outcome, 0, sizeof(outcome));

    pool_createwhatprovides(pool);

    Queue todo, installed;
    queue_init(&todo);
    queue_init(&installed);

    Map installed_map, visited_reqs;
    map_init(&installed_map, pool->nsolvables);
    map_init(&visited_reqs, pool->ss.nstrings);

    for (int i = 0; i < num_reqs; i++) {
        Id req_id = pool_str2id(pool, reqs[i], 1);
        queue_push(&todo, req_id);
    }

    while (todo.count > 0) {
        Id req_id = queue_shift(&todo);
        if (req_id < pool->ss.nstrings && MAPTST(&visited_reqs, req_id)) {
            continue;
        }
        if (req_id < pool->ss.nstrings) {
            MAPSET(&visited_reqs, req_id);
        }

        Queue plist;
        queue_init(&plist);
        Id p, d;
        FOR_PROVIDES(p, d, req_id) {
            queue_push(&plist, p);
        }

        if (plist.count == 0) {
            outcome.status = BSGO_SOLV_ERR_NOPROVIDER;
            outcome.err_detail = strdup(pool_id2str(pool, req_id));
            queue_free(&plist);
            goto cleanup;
        }

        Id chosen = 0;
        // Check if already satisfied by installed package
        for (int i = 0; i < plist.count; i++) {
            if (MAPTST(&installed_map, plist.elements[i])) {
                chosen = plist.elements[i];
                break;
            }
        }

        if (!chosen) {
            int ambiguous = 0;
            chosen = select_provider(pool, &plist, num_prefers, prefers, &ambiguous);
            if (ambiguous) {
                outcome.status = BSGO_SOLV_ERR_CHOICE;
                outcome.err_detail = strdup(pool_id2str(pool, req_id));
                queue_free(&plist);
                goto cleanup;
            }
        }
        queue_free(&plist);

        if (!MAPTST(&installed_map, chosen)) {
            MAPSET(&installed_map, chosen);
            queue_push(&installed, chosen);

            Solvable *s = pool->solvables + chosen;
            if (s->requires) {
                Id *rp = s->repo->idarraydata + s->requires;
                while (*rp) {
                    queue_push(&todo, *rp);
                    rp++;
                }
            }
        }
    }

    // Topological sort
    Queue sorted;
    queue_init(&sorted);
    toposort_solvables(pool, &installed, &sorted);

    outcome.status = BSGO_SOLV_OK;
    outcome.package_count = sorted.count;
    outcome.packages = malloc(sizeof(ResolvedPackage) * sorted.count);
    for (int i = 0; i < sorted.count; i++) {
        Solvable *s = pool->solvables + sorted.elements[i];
        outcome.packages[i].name = strdup(pool_id2str(pool, s->name));
        outcome.packages[i].evr = strdup(pool_id2str(pool, s->evr));
        outcome.packages[i].arch = strdup(pool_id2str(pool, s->arch));
    }
    queue_free(&sorted);

cleanup:
    queue_free(&todo);
    queue_free(&installed);
    map_free(&installed_map);
    map_free(&visited_reqs);
    return outcome;
}

void bsgo_free_outcome(ExpansionOutcome *outcome) {
    if (!outcome) return;
    if (outcome->err_detail) {
        free(outcome->err_detail);
        outcome->err_detail = NULL;
    }
    if (outcome->packages) {
        for (int i = 0; i < outcome->package_count; i++) {
            free(outcome->packages[i].name);
            free(outcome->packages[i].evr);
            free(outcome->packages[i].arch);
        }
        free(outcome->packages);
        outcome->packages = NULL;
    }
}
