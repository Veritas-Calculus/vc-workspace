#!/usr/bin/env python3
"""Unprivileged XFCE display integration for Broker-managed local users.

Keep panel geometry in step with the Broker's DPI, and bring normal windows
back into the work area when a single-screen RDP desktop shrinks. Never reset
panel composition, wallpaper, custom themes, focus or maximized window state.
"""

import os
import pwd
import re
import subprocess
from pathlib import Path


def run(*args):
    try:
        return subprocess.run(args, capture_output=True, text=True, timeout=2,
                              check=True).stdout.strip()
    except (OSError, subprocess.SubprocessError):
        return None


def query(channel, prop):
    return run("xfconf-query", "-c", channel, "-p", prop)


def set_property(channel, prop, kind, value):
    return run("xfconf-query", "-c", channel, "-p", prop, "-n", "-t", kind,
               "-s", str(value)) is not None


def scaled_size(value, previous, target, limit=128):
    # Zero is XFCE's automatic icon size, not a missing value.
    return min(limit, max(0, round(value * target / previous)))


def fit_window(window, area, margin=16):
    x, y, width, height = window
    ax, ay, aw, ah = area
    width = min(width, max(1, aw - margin * 2))
    height = min(height, max(1, ah - margin * 2))
    return (min(max(x, ax + margin), ax + aw - width - margin),
            min(max(y, ay + margin), ay + ah - height - margin), width, height)


def fit_decorated_window(client, extents, area):
    x, y, width, height = client
    left, right, top, bottom = extents
    frame = (x - left, y - top, width + left + right, height + top + bottom)
    fitted = fit_window(frame, area)
    if fitted == frame:
        return None
    return (fitted[0], fitted[1], max(1, fitted[2] - left - right),
            max(1, fitted[3] - top - bottom))


def apply_scale(target):
    properties = run("xfconf-query", "-c", "xfce4-panel", "-l") or ""
    sizes = [p for p in properties.splitlines()
             if re.fullmatch(r"/panels/panel-\d+/(size|icon-size)", p)]
    if not sizes:  # XFCE hasn't loaded its default panel configuration yet.
        return False
    previous = query("vc-workspace", "/display-scale")
    previous = int(previous) if previous in ("1", "2") else 1
    if previous == target:
        return True
    changes = []
    for prop in sizes:
        value = query("xfce4-panel", prop)
        if value is not None and value.isdigit():
            changes.append(("xfce4-panel", prop, "uint",
                            scaled_size(int(value), previous, target)))
    icon = query("xfce4-desktop", "/desktop-icons/icon-size") or "48"
    if icon.isdigit():
        changes.append(("xfce4-desktop", "/desktop-icons/icon-size", "uint",
                        scaled_size(int(icon), previous, target, 256)))
    cursor = query("xsettings", "/Gtk/CursorThemeSize") or "24"
    if cursor.isdigit():
        changes.append(("xsettings", "/Gtk/CursorThemeSize", "int",
                        scaled_size(int(cursor), previous, target, 256)))
    theme = query("xfwm4", "/general/theme")
    if theme in (None, "", "Default", "Default-xhdpi"):
        changes.append(("xfwm4", "/general/theme", "string",
                        "Default-xhdpi" if target == 2 else "Default"))
    # Roll back partial application so a retry cannot double an already changed
    # panel. Only advance the marker once every requested property succeeded.
    changes.append(("vc-workspace", "/display-scale", "int", target))
    applied = []
    for channel, prop, kind, value in changes:
        old = query(channel, prop)
        if not set_property(channel, prop, kind, value):
            for c, p, k, v in reversed(applied):
                if v is None:
                    run("xfconf-query", "-c", c, "-p", p, "-r")
                else:
                    set_property(c, p, k, v)
            return False
        applied.append((channel, prop, kind, old))
    return True


def recover_windows(area):
    windows = run("wmctrl", "-lG") or ""
    for line in windows.splitlines():
        fields = line.split(None, 7)
        if len(fields) < 7 or not re.fullmatch(r"0x[0-9a-fA-F]+", fields[0]):
            continue
        xid = fields[0]
        state = run("xprop", "-id", xid, "_NET_WM_WINDOW_TYPE", "_NET_WM_STATE", "_NET_FRAME_EXTENTS") or ""
        if not any(t in state for t in ("_NET_WM_WINDOW_TYPE_NORMAL", "_NET_WM_WINDOW_TYPE_DIALOG")):
            continue  # Never move panels, docks, menus or the desktop itself.
        if any(t in state for t in ("_NET_WM_STATE_MAXIMIZED", "_NET_WM_STATE_FULLSCREEN", "_NET_WM_STATE_HIDDEN")):
            continue
        # wmctrl -lG adds the reparenting offset twice on XFWM. Read absolute
        # client coordinates from X instead and include decorations in bounds.
        geometry = run("xwininfo", "-id", xid, "-stats") or ""
        values = [re.search(r"^\s*" + label + r":\s*(-?\d+)", geometry, re.M)
                  for label in ("Absolute upper-left X", "Absolute upper-left Y", "Width", "Height")]
        if not all(values):
            continue
        original = tuple(int(v.group(1)) for v in values)
        extents = re.search(r"_NET_FRAME_EXTENTS\(CARDINAL\) = (\d+), (\d+), (\d+), (\d+)", state)
        borders = tuple(map(int, extents.groups())) if extents else (0, 0, 0, 0)
        fitted = fit_decorated_window(original, borders, area)
        if fitted is not None:
            run("wmctrl", "-i", "-r", xid, "-e", "0," + ",".join(map(str, fitted)))


def main():
    username = pwd.getpwuid(os.getuid()).pw_name
    if not re.fullmatch(r"vcw[0-9a-f]{12}", username) or not os.environ.get("DISPLAY"):
        return
    request = Path("/var/lib/vc-workspace/display-scale") / username
    if not request.is_file():
        return
    import gi
    gi.require_version("Gdk", "3.0")
    from gi.repository import Gdk, GLib

    screen = Gdk.Screen.get_default()
    display = Gdk.Display.get_default()
    if screen is None or display is None:
        return
    previous_size = [screen.get_width(), screen.get_height()]
    pending = [None]

    def recover():
        pending[0] = None
        if display.get_n_monitors() == 1:
            monitor = display.get_monitor(0)
            rect = monitor.get_workarea()
            factor = monitor.get_scale_factor()
            recover_windows(tuple(v * factor for v in (rect.x, rect.y, rect.width, rect.height)))
        return False

    def changed(*_):
        width, height = screen.get_width(), screen.get_height()
        if width < previous_size[0] or height < previous_size[1]:
            if pending[0] is not None:
                GLib.source_remove(pending[0])
            pending[0] = GLib.timeout_add(600, recover)
        previous_size[:] = width, height

    last_scale = [None]

    def sync_scale():
        try:
            raw = request.read_text().strip()
        except OSError:
            return True
        if raw in ("100", "200") and raw != last_scale[0]:
            if apply_scale(int(raw) // 100):
                last_scale[0] = raw
        return True

    screen.connect("size-changed", changed)
    sync_scale()
    GLib.timeout_add_seconds(2, sync_scale)
    GLib.MainLoop().run()


if __name__ == "__main__":
    main()
