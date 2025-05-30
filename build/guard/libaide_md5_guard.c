#define _GNU_SOURCE
#include <dlfcn.h>
#include <gcrypt.h>
#include <gpg-error.h>
#include <pthread.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>

/* We intercept gcry_md_open and gcry_md_enable */
static gcry_error_t (*real_md_open)(gcry_md_hd_t *, int, unsigned int);
static gcry_error_t (*real_md_enable)(gcry_md_hd_t, int);

/* One-time sync for resolving symbols */
static pthread_once_t once = PTHREAD_ONCE_INIT;

/*
 * Small async-safe logger that writes directly to stderr,
 * avoiding recursion if we're hooking I/O calls.
 */
__attribute__((format(printf, 1, 2)))
static void logln(const char *fmt, ...)
{
    char buf[256];
    va_list ap;
    va_start(ap, fmt);
    int n = vsnprintf(buf, sizeof(buf), fmt, ap);
    va_end(ap);
    if (n > (int)sizeof(buf) - 1) {
        n = (int)sizeof(buf) - 1;
    }
    buf[n++] = '\n';
    write(STDERR_FILENO, buf, n);
}

/*
 * We only resolve the real gcry_md_* symbols once.
 * No references to gcry_fips_mode_active here, to avoid symbol issues.
 */
static void resolve_symbols(void)
{
    real_md_open   = dlsym(RTLD_NEXT, "gcry_md_open");
    real_md_enable = dlsym(RTLD_NEXT, "gcry_md_enable");
}

/*
 * We check the kernel's FIPS flag: /proc/sys/crypto/fips_enabled
 * If it's '1', we treat the system as in FIPS mode.
 */
static int in_fips_mode(void)
{
    FILE *fp = fopen("/proc/sys/crypto/fips_enabled", "r");
    if (!fp) {
        // If missing, assume not in FIPS
        return 0;
    }
    char c = '0';
    if (fread(&c, 1, 1, fp) != 1) {
        fclose(fp);
        return 0;
    }
    fclose(fp);
    return (c == '1');
}

static int strict_mode(void)
{
    /* Any non-empty value triggers strict termination.  We use secure_getenv
     * so unprivileged callers can’t influence set-uid binaries.               */
    return secure_getenv("AIDE_GUARD_STRICT") != NULL;
}

/* -------------------------------------------------------------------------- */
/*  gcry_md_open shim                                                         */
gcry_error_t gcry_md_open(gcry_md_hd_t *hd, int algo, unsigned int flags)
{
    pthread_once(&once, resolve_symbols);
    if (!real_md_open) {
        /* Fallback resolution if needed */
        real_md_open = dlsym(RTLD_DEFAULT, "gcry_md_open");
    }
    if (!real_md_open) {
        /* If still not found, cannot proceed */
        return GPG_ERR_NOT_SUPPORTED;
    }

    /* Outside FIPS => allow everything */
    if (!in_fips_mode()) {
        return real_md_open(hd, algo, flags);
    }

    /* In FIPS mode => allow empty multi-hash context (algo=0) */
    if (algo == 0) {
        return real_md_open(hd, 0, flags);
    }

    if (algo == GCRY_MD_MD5) {
        if (strict_mode())
        {
            logln("[md-guard] MD5 requested in FIPS - Strict mode – terminating");
            _exit(64);
        }
        logln("[md-guard] MD5 requested in FIPS – soft-blocking");
        /* Soft block: MD5 will not be enabled */
        return GPG_ERR_NOT_SUPPORTED;
    }
    return real_md_open(hd, algo, flags);
}

/*
 * Hook for gcry_md_enable:
 * - If not in FIPS mode, pass through.
 * - If in FIPS and algo=MD5, block.
 * - Otherwise, allow.
 */
gcry_error_t gcry_md_enable(gcry_md_hd_t hd, int algo)
{
    pthread_once(&once, resolve_symbols);
    if (!real_md_enable) {
        real_md_enable = dlsym(RTLD_DEFAULT, "gcry_md_enable");
    }
    if (!real_md_enable) {
        return GPG_ERR_NOT_SUPPORTED;
    }

    /* Outside FIPS => no block */
    if (!in_fips_mode()) {
        return real_md_enable(hd, algo);
    }
    if (algo == GCRY_MD_MD5) {
        if (strict_mode())
        {
            logln("[md-guard] MD5 enable detected in FIPS - Strict mode – terminating");
            _exit(64);
        }
        logln("[md-guard] MD5 enable blocked in FIPS – soft-blocking");
        return GPG_ERR_NOT_SUPPORTED;
    }
    return real_md_enable(hd, algo);
}