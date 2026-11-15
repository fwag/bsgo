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

Id bsgo_str2dep(Pool *pool, const char *s);
int bsgo_pool_prepare(Pool *pool);
int bsgo_whatprovides(Pool *pool, Id dep, Id **out);
int bsgo_solvable_info(Pool *pool, Id p, BsgoSolvInfo *out);
int bsgo_solvable_deps(Pool *pool, Id p, int which, Id **out);
int bsgo_dep_decompose(Pool *pool, Id dep, Id *name, Id *evr, int *flags);
const char *bsgo_dep2str(Pool *pool, Id dep);
const char *bsgo_id2str(Pool *pool, Id id);

#endif
