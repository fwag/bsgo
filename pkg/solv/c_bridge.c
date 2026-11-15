#define _GNU_SOURCE
#include "c_bridge.h"
#include <solv/poolarch.h>
#include <solv/repo.h>

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

    // Prune foreign architecture solvables so they are not indexed into provides table
    if (pool->id2arch) {
        for (Id p = repo->start; p < repo->end; p++) {
            Solvable *s = pool->solvables + p;
            if (s->repo == repo && pool_badarch_solvable(pool, s)) {
                s->provides = 0;
                s->requires = 0;
            }
        }
    }

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

static Id select_provider(Pool *pool, Queue *plist, int num_prefers, const char **prefers, int *ambiguous, const char *req_name) {
    *ambiguous = 0;
    if (plist->count == 0) return 0;
    if (plist->count == 1) return plist->elements[0];

    // 0. Filter by architecture score (prefer native architecture over subarch/foreign)
    if (pool->id2arch) {
        unsigned int best_score = 0;
        for (int i = 0; i < plist->count; i++) {
            Solvable *s = pool->solvables + plist->elements[i];
            unsigned int score = pool_arch2score(pool, s->arch);
            if (score > 0 && (best_score == 0 || score < best_score)) {
                best_score = score;
            }
        }
        if (best_score > 0) {
            Queue best_q;
            queue_init(&best_q);
            for (int i = 0; i < plist->count; i++) {
                Solvable *s = pool->solvables + plist->elements[i];
                if (pool_arch2score(pool, s->arch) == best_score) {
                    queue_push(&best_q, plist->elements[i]);
                }
            }
            if (best_q.count == 1) {
                Id chosen = best_q.elements[0];
                queue_free(&best_q);
                return chosen;
            }
            if (best_q.count > 0 && best_q.count < plist->count) {
                queue_empty(plist);
                for (int i = 0; i < best_q.count; i++) {
                    queue_push(plist, best_q.elements[i]);
                }
            }
            queue_free(&best_q);
        }
    }

    // 1. If all remaining candidates share the exact same package name, choose the first (e.g. multi-version)
    const char *first_name = pool_id2str(pool, (pool->solvables + plist->elements[0])->name);
    int all_same_name = 1;
    for (int i = 1; i < plist->count; i++) {
        const char *sname = pool_id2str(pool, (pool->solvables + plist->elements[i])->name);
        if (!sname || strcmp(sname, first_name) != 0) {
            all_same_name = 0;
            break;
        }
    }
    if (all_same_name) {
        return plist->elements[0];
    }

    // 2. Exact name match: if requirement name matches a candidate package name
    if (req_name && *req_name) {
        for (int i = 0; i < plist->count; i++) {
            Solvable *s = pool->solvables + plist->elements[i];
            const char *sname = pool_id2str(pool, s->name);
            if (sname && strcmp(sname, req_name) == 0) {
                return plist->elements[i];
            }
        }
    }

    // 3. Priority-ordered matching against prefer list
    for (int j = 0; j < num_prefers; j++) {
        if (!prefers[j] || prefers[j][0] == '-') {
            continue; // Skip empty or negative prefers (e.g. -package)
        }
        for (int i = 0; i < plist->count; i++) {
            Solvable *s = pool->solvables + plist->elements[i];
            const char *sname = pool_id2str(pool, s->name);
            if (sname && strcmp(sname, prefers[j]) == 0) {
                return plist->elements[i];
            }
        }
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
                             int num_prefers, const char **prefers,
                             int num_ignores, const char **ignores) {
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
        if (req_id <= 0 || req_id == SOLVABLE_PREREQMARKER || req_id == SOLVABLE_FILEMARKER) {
            continue;
        }
        if (req_id < pool->ss.nstrings && MAPTST(&visited_reqs, req_id)) {
            continue;
        }
        if (req_id < pool->ss.nstrings) {
            MAPSET(&visited_reqs, req_id);
        }

        const char *req_name = pool_id2str(pool, req_id);
        if (req_name && num_ignores > 0) {
            int is_ignored = 0;
            for (int i = 0; i < num_ignores; i++) {
                if (!ignores[i]) continue;
                if (strcmp(ignores[i], req_name) == 0) {
                    is_ignored = 1;
                    break;
                }
                const char *colon = strchr(ignores[i], ':');
                if (colon && strcmp(colon + 1, req_name) == 0) {
                    is_ignored = 1;
                    break;
                }
            }
            if (is_ignored) {
                continue;
            }
        }

        Queue plist;
        queue_init(&plist);
        Id p, d;
        FOR_PROVIDES(p, d, req_id) {
            Solvable *s = pool->solvables + p;
            if (!pool_installable(pool, s)) {
                continue;
            }
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
            const char *req_name = pool_id2str(pool, req_id);
            chosen = select_provider(pool, &plist, num_prefers, prefers, &ambiguous, req_name);
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
                    if (*rp != SOLVABLE_PREREQMARKER && *rp != SOLVABLE_FILEMARKER) {
                        queue_push(&todo, *rp);
                    }
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
    if (sorted.count > 0) {
        outcome.packages = malloc(sizeof(ResolvedPackage) * sorted.count);
        if (!outcome.packages) {
            outcome.status = -1;
            outcome.err_detail = strdup("out of memory allocating packages");
            queue_free(&sorted);
            goto cleanup;
        }
        for (int i = 0; i < sorted.count; i++) {
            Solvable *s = pool->solvables + sorted.elements[i];
            const char *name = pool_id2str(pool, s->name);
            const char *evr = pool_id2str(pool, s->evr);
            const char *arch = pool_id2str(pool, s->arch);
            const char *sha = NULL;
            if (s->repo && s->repo->nrepodata > 0) {
                Id chktype = 0;
                sha = solvable_lookup_checksum(s, SOLVABLE_CHECKSUM, &chktype);
            }
            outcome.packages[i].name = name ? strdup(name) : strdup("");
            outcome.packages[i].evr = evr ? strdup(evr) : strdup("");
            outcome.packages[i].arch = arch ? strdup(arch) : strdup("");
            outcome.packages[i].sha256 = sha ? strdup(sha) : strdup("");
        }
    } else {
        outcome.packages = NULL;
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
            free(outcome->packages[i].sha256);
        }
        free(outcome->packages);
        outcome->packages = NULL;
    }
}
