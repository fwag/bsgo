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
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define BSGO_SOLV_OK 0
#define BSGO_SOLV_ERR_NOPROVIDER 1
#define BSGO_SOLV_ERR_CHOICE 2
#define BSGO_SOLV_ERR_CONFLICT 3

typedef struct {
    char *name;
    char *evr;
    char *arch;
} ResolvedPackage;

typedef struct {
    int status;
    char *err_detail;
    int package_count;
    ResolvedPackage *packages;
} ExpansionOutcome;

Pool *bsgo_pool_create(const char *arch);
void bsgo_pool_free(Pool *pool);

Repo *bsgo_repo_create(Pool *pool, const char *name);
Repo *bsgo_load_repo_from_mem(Pool *pool, const char *name, const void *buf, size_t len);
int bsgo_dump_repo_to_mem(Repo *repo, void **out_buf, size_t *out_len);

int bsgo_add_package(Repo *repo, const char *name, const char *evr, const char *arch,
                     int num_provides, const char **provides,
                     int num_requires, const char **requires);

ExpansionOutcome bsgo_expand(Pool *pool,
                             int num_reqs, const char **reqs,
                             int num_prefers, const char **prefers);

void bsgo_free_outcome(ExpansionOutcome *outcome);

#endif
