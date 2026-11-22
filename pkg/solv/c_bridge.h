#ifndef BSGO_SOLV_C_BRIDGE_H
#define BSGO_SOLV_C_BRIDGE_H

#include <solv/pool.h>
#include <solv/repo.h>
#include <solv/repo_solv.h>
#include <solv/repo_write.h>
#include <solv/solvable.h>
#include <solv/strpool.h>
#include <solv/queue.h>
#include <solv/bitmap.h>
#include <solv/repo_rpmmd.h>
#include <solv/poolarch.h>
#include <solv/knownid.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    const char *name;
    const char *evr;
    const char *arch;
    const char *checksum;
    int repo;
    int sha256;
} BsgoSolvInfo;

Pool *bsgo_pool_create(const char *arch);
void bsgo_pool_free(Pool *pool);

Repo *bsgo_repo_create(Pool *pool, const char *name);
Repo *bsgo_load_repo_from_mem(Pool *pool, const char *name, const void *buf, size_t len);
Repo *bsgo_load_repo_from_rpmmd_mem(Pool *pool, const char *name, const void *buf, size_t len);
int bsgo_dump_repo_to_mem(Repo *repo, void **out_buf, size_t *out_len);

int bsgo_add_package(Repo *repo, const char *name, const char *evr, const char *arch,
                     int num_provides, const char **provides,
                     int num_requires, const char **requires);

/* Dependency kinds for bsgo_solvable_add_dep. */
enum {
    BSGO_DEP_PROVIDES = 0, BSGO_DEP_REQUIRES, BSGO_DEP_CONFLICTS, BSGO_DEP_OBSOLETES,
    BSGO_DEP_RECOMMENDS, BSGO_DEP_SUGGESTS, BSGO_DEP_SUPPLEMENTS, BSGO_DEP_ENHANCES
};

Id bsgo_repo_add_solvable(Repo *repo, const char *name, const char *evr, const char *arch);
int bsgo_solvable_add_dep(Repo *repo, Id sid, int kind, const char *name, const char *op, const char *evr, int pre);
int bsgo_solvable_set_sha256(Repo *repo, Id sid, const char *hex);
int bsgo_solvable_add_file(Repo *repo, Id sid, const char *path);
int bsgo_repo_finish(Repo *repo);
int bsgo_solvable_set_unit(Repo *repo, Id sid, const char *unit);
int bsgo_repo_remove_unit(Repo *repo, const char *unit);
char *bsgo_all_provides(Pool *pool);
char *bsgo_all_requires(Pool *pool);

Id bsgo_str2dep(Pool *pool, const char *s);
int bsgo_pool_prepare(Pool *pool);
int bsgo_whatprovides(Pool *pool, Id dep, Id **out);
int bsgo_solvable_info(Pool *pool, Id p, BsgoSolvInfo *out);
const char *bsgo_solvable_location(Pool *pool, Id p);
int bsgo_solvable_deps(Pool *pool, Id p, int which, Id **out);
int bsgo_dep_decompose(Pool *pool, Id dep, Id *name, Id *evr, int *flags);
const char *bsgo_dep2str(Pool *pool, Id dep);
const char *bsgo_id2str(Pool *pool, Id id);

#endif
