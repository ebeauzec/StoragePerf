#!/usr/bin/env python3
"""Builds a release tar.gz with explicit, correct Unix permission bits.

Exists because this repo is built from Git Bash on Windows against a
Google-Drive-synced checkout, where `chmod` is a confirmed no-op: `mount`
reports the drive as `vfat (...,noacl,posix=0,...)`, and even a plain local
NTFS path under this Git-for-Windows install behaves the same way (verified
live -- chmod +x on a fresh file produces no visible or archived effect).
GNU tar and `ls` then infer a file's executable bit purely from whether it
starts with a `#!` shebang line, since there's no real POSIX mode bit to
read -- which silently ships a prebuilt Unix binary (no shebang) as
non-executable (644) regardless of any chmod call. This sidesteps the OS
permission layer entirely: Python's tarfile module lets each archive
entry's mode be set explicitly and directly, independent of whatever the
host filesystem reports.
"""
import os
import sys
import tarfile

EXECUTABLE_NAMES = {"plumb", "start.sh", "prometheus", "victoria-metrics"}


def main():
    if len(sys.argv) != 4:
        print("usage: make-tar.py <src_dir> <archive_path> <top_level_name>", file=sys.stderr)
        return 1
    src_dir, archive_path, top_name = sys.argv[1], sys.argv[2], sys.argv[3]

    def set_mode(tarinfo):
        if tarinfo.isdir():
            tarinfo.mode = 0o755
        elif os.path.basename(tarinfo.name) in EXECUTABLE_NAMES:
            tarinfo.mode = 0o755
        else:
            tarinfo.mode = 0o644
        # Deterministic ownership -- this checkout's own uid/gid on a
        # Windows/Drive-synced mount is meaningless to whoever extracts
        # this on a real Unix machine.
        tarinfo.uid = 0
        tarinfo.gid = 0
        tarinfo.uname = ""
        tarinfo.gname = ""
        return tarinfo

    with tarfile.open(archive_path, "w:gz") as tf:
        tf.add(src_dir, arcname=top_name, filter=set_mode)
    return 0


if __name__ == "__main__":
    sys.exit(main())
