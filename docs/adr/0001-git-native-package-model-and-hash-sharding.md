# 0001: Git-Native Package Model and Hash Sharding

We decided to model all source inputs directly as Git repositories and replace legacy CPIO/OBS versioning with Git commits and Git LFS. Within project repositories, package manifests are stored across 256 subdirectories partitioned by the first two hex characters of the package name's SHA-256 hash (`packages/xx/<package>.yaml`).

This eliminates legacy `bs_srcserver`, custom VCS formats, and `scmsync` converters, while preventing directory tree bloat, Git merge conflicts, and filesystem inode limits when managing large distributions like openSUSE Factory (16,000+ packages).
