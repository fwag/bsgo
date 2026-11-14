import glob
import os
import subprocess
import sys


def main():
    active_jobs = set()
    for p in glob.glob('/proc/*/fd/*'):
        try:
            target = os.readlink(p)
            if '/var/tmp/bsgo-worker/jobs/' in target:
                job_id = (
                    target.split('/var/tmp/bsgo-worker/jobs/')[1].split('/')[0]
                )
                active_jobs.add(job_id)
        except Exception:
            pass

    print(f'Active jobs to preserve ({len(active_jobs)}): {active_jobs}')

    obs_build = '/home/ta/Devel/bsgo/bin/build-sudo'
    count = 0
    reclaimed = 0

    for job_dir in sorted(glob.glob('/var/tmp/bsgo-worker/jobs/*')):
        job_id = os.path.basename(job_dir)
        if job_id in active_jobs:
            continue

        build_root = os.path.join(job_dir, 'build-root')
        if os.path.exists(build_root):
            subprocess.run(
                [obs_build, '--root', build_root, '--wipe'],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
            )
            count += 1

        repo_snap = os.path.join(job_dir, 'repo_snapshot')
        if os.path.exists(repo_snap):
            subprocess.run(['rm', '-rf', repo_snap])

        pkgs = os.path.join(job_dir, 'packages')
        if os.path.exists(pkgs):
            subprocess.run(['rm', '-rf', pkgs])

    print(f'Wiped {count} old build roots.')


if __name__ == '__main__':
    main()
