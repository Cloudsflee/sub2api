#!/bin/sh
set -eu

display=:99
lock_file=/tmp/.X99-lock
socket_file=/tmp/.X11-unix/X99
log_file=/tmp/xvfb.log

# Docker restarts reuse the container filesystem. Xvfb cannot start when the
# previous process left its lock or socket behind, so remove only display 99's
# stale files before launching the worker-owned server.
rm -f "$lock_file" "$socket_file"
Xvfb "$display" -screen 0 2560x1600x24 -ac +extension RANDR -nolisten tcp >"$log_file" 2>&1 &
xvfb_pid=$!

# Do not race Camoufox against Xvfb startup. xdotool makes a real X11 request,
# which is stronger than merely observing that the Unix socket exists.
attempt=0
until DISPLAY="$display" xdotool getmouselocation >/dev/null 2>&1; do
  if ! kill -0 "$xvfb_pid" 2>/dev/null; then
    cat "$log_file" >&2 || true
    wait "$xvfb_pid" || true
    exit 1
  fi
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 100 ]; then
    echo "Xvfb did not become ready on $display" >&2
    cat "$log_file" >&2 || true
    kill "$xvfb_pid" 2>/dev/null || true
    wait "$xvfb_pid" || true
    exit 1
  fi
  sleep 0.05
done

export DISPLAY="$display"
exec node index.js
