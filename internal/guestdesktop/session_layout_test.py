import unittest
from unittest.mock import patch

import session_layout as layout


class GeometryTests(unittest.TestCase):
    def test_visible_window_unchanged(self):
        self.assertEqual(layout.fit_window((40, 80, 800, 600), (0, 52, 1920, 1028)),
                         (40, 80, 800, 600))

    def test_5k_window_fits_after_shrink(self):
        self.assertEqual(layout.fit_window((3600, 1800, 1300, 900), (0, 52, 1360, 792)),
                         (44, 68, 1300, 760))

    def test_negative_and_oversized_window(self):
        self.assertEqual(layout.fit_window((-500, -200, 2000, 1500), (0, 52, 1360, 792)),
                         (16, 68, 1328, 760))

    def test_retina_round_trip(self):
        for size in (0, 16, 26, 48):
            self.assertEqual(layout.scaled_size(layout.scaled_size(size, 1, 2), 2, 1), size)

    def test_size_bounded(self):
        self.assertEqual(layout.scaled_size(100, 1, 2), 128)

    def test_hidpi_window_decorations_stay_inside_workarea(self):
        self.assertEqual(layout.fit_decorated_window((3612, 1858, 1195, 688), (12, 12, 58, 12), (0, 53, 2000, 1207)),
                         (765, 486, 1195, 688))
        self.assertIsNone(layout.fit_decorated_window((52, 138, 800, 600), (12, 12, 58, 12), (0, 53, 2000, 1207)))


class ScaleTests(unittest.TestCase):
    def setUp(self):
        self.values = {("xfce4-panel", "/panels/panel-1/size"): "26",
                       ("xfce4-panel", "/panels/panel-1/icon-size"): "16",
                       ("xfce4-panel", "/panels/panel-2/size"): "48",
                       ("xfwm4", "/general/theme"): "Default"}
        self.fail = None
        self.failed = False

    def query(self, channel, prop):
        return self.values.get((channel, prop))

    def set_value(self, channel, prop, kind, value):
        if (channel, prop) == self.fail and not self.failed:
            self.failed = True
            return False
        self.values[channel, prop] = str(value)
        return True

    def command(self, *args):
        if "-l" in args:
            return "\n".join(p for (c, p) in self.values if c == "xfce4-panel")
        if "-r" in args:
            self.values.pop((args[2], args[4]), None)
        return ""

    def apply(self, target):
        with patch.object(layout, "query", self.query), patch.object(layout, "set_property", self.set_value), patch.object(layout, "run", self.command):
            return layout.apply_scale(target)

    def test_retina_panel_and_idempotent_reconnect(self):
        self.assertTrue(self.apply(2))
        self.assertEqual(self.values["xfce4-panel", "/panels/panel-1/size"], "52")
        self.assertEqual(self.values["xfce4-panel", "/panels/panel-2/size"], "96")
        self.assertEqual(self.values["xfwm4", "/general/theme"], "Default-xhdpi")
        once = self.values.copy()
        self.assertTrue(self.apply(2))
        self.assertEqual(self.values, once)
        self.assertTrue(self.apply(1))
        self.assertEqual(self.values["xfce4-panel", "/panels/panel-1/size"], "26")

    def test_custom_theme_preserved(self):
        self.values["xfwm4", "/general/theme"] = "UserTheme"
        self.assertTrue(self.apply(2))
        self.assertEqual(self.values["xfwm4", "/general/theme"], "UserTheme")

    def test_wait_for_panel_initialization(self):
        self.values = {}
        self.assertFalse(self.apply(2))
        self.assertEqual(self.values, {})

    def test_partial_failure_and_marker_failure_roll_back(self):
        for failed_property in (("xfce4-panel", "/panels/panel-2/size"), ("vc-workspace", "/display-scale")):
            with self.subTest(property=failed_property):
                self.setUp()
                before = self.values.copy()
                self.fail = failed_property
                self.assertFalse(self.apply(2))
                self.assertEqual(self.values, before)
                self.assertTrue(self.apply(2))
                self.assertEqual(self.values["xfce4-panel", "/panels/panel-1/size"], "52")


class WindowTests(unittest.TestCase):
    def test_only_normal_windows_move_without_activation(self):
        commands = []

        def run(*args):
            commands.append(args)
            if args == ("wmctrl", "-lG"):
                return "\n".join(f"0x00000{i:03x} 0 3600 1800 1300 900 host window" for i in range(1, 6))
            if args[0] == "xprop":
                return {"0x00000001": "_NET_WM_WINDOW_TYPE_NORMAL", "0x00000002": "_NET_WM_WINDOW_TYPE_DOCK",
                        "0x00000003": "_NET_WM_WINDOW_TYPE_NORMAL _NET_WM_STATE_MAXIMIZED_VERT",
                        "0x00000004": "_NET_WM_WINDOW_TYPE_NORMAL _NET_WM_STATE_FULLSCREEN",
                        "0x00000005": "_NET_WM_WINDOW_TYPE_NORMAL _NET_WM_STATE_HIDDEN"}[args[2]]
            if args[0] == "xwininfo":
                return "Absolute upper-left X: 3600\nAbsolute upper-left Y: 1800\nWidth: 1300\nHeight: 900"
            return ""

        with patch.object(layout, "run", run):
            layout.recover_windows((0, 52, 1360, 792))
        self.assertEqual([c for c in commands if c[:2] == ("wmctrl", "-i")],
                         [("wmctrl", "-i", "-r", "0x00000001", "-e", "0,44,68,1300,760")])


if __name__ == "__main__":
    unittest.main()
