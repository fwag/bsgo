# bsgo single-host deployment with systemd user units

For a test host (e.g. aisrv) where all bsgo services run as one user.

## One-time setup (as root)

    # start the user services at boot without a login session
    loginctl enable-linger <user>

    # obs-build runs as root; the worker also terminates its own nspawn build
    # containers on shutdown/abort
    cat > /etc/sudoers.d/bsgo-worker <<'SUDO'
    <user> ALL=(root) NOPASSWD: /usr/bin/build
    <user> ALL=(root) NOPASSWD: /usr/bin/machinectl terminate obsbuild.*
    SUDO
    chmod 0440 /etc/sudoers.d/bsgo-worker && visudo -c

## As the user

    # restart the Podman containers at boot
    for c in bsgo-postgres bsgo-nats bsgo-garage; do podman update --restart always $c; done
    systemctl --user enable --now podman-restart.service

    mkdir -p ~/.config/systemd/user
    cp ~/Devel/bsgo/hack/systemd/*.service ~/.config/systemd/user/
    systemctl --user daemon-reload
    systemctl --user enable bsgo-repo-slfo11 bsgo-signd bsgo-server bsgo-worker

`systemctl --user restart bsgo-server` / `bsgo-worker` replaces the manual
`nohup` starts. Logs stay in `~/Devel/bsgo/server.log` and `worker.log`.
