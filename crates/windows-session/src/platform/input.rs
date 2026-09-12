//! Literal text and allowlisted virtual-key chords, not expression parsing,
//! clipboard replacement or UIA value writes. Win32 unsafe code stays here.
use super::*;
use windows_sys::Win32::UI::Input::KeyboardAndMouse::*;

fn unicode_inputs(text: &str) -> io::Result<Vec<INPUT>> {
    if text.is_empty() || text.contains('\0') || text.chars().take(33).count() > 32 {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "bounded Unicode text chunk required",
        ));
    }
    let mut events = Vec::with_capacity(text.encode_utf16().count() * 2);
    for unit in text.encode_utf16() {
        for flags in [KEYEVENTF_UNICODE, KEYEVENTF_UNICODE | KEYEVENTF_KEYUP] {
            events.push(INPUT {
                r#type: INPUT_KEYBOARD,
                Anonymous: INPUT_0 {
                    ki: KEYBDINPUT {
                        wVk: 0,
                        wScan: unit,
                        dwFlags: flags,
                        time: 0,
                        dwExtraInfo: 0,
                    },
                },
            });
        }
    }
    Ok(events)
}

/// Caller must recheck the bound control authority before EACH chunk. SendInput
/// reports insertion, not application acceptance. A partial insertion is an
/// uncertain side effect and must never be automatically typed again.
pub fn send_unicode_text_chunk(text: &str) -> io::Result<()> {
    let events = unicode_inputs(text)?;
    super::desktop::ensure_input_desktop()?;
    require_released_modifiers()?;
    let inserted = insert(&events);
    if inserted != events.len() {
        return Err(io::Error::other(
            "Windows text input was not fully inserted",
        ));
    }
    Ok(())
}

fn require_released_modifiers() -> io::Result<()> {
    for key in [VK_CONTROL, VK_MENU, VK_SHIFT, VK_LWIN, VK_RWIN] {
        // SAFETY: value-only key query in the already checked user desktop.
        if unsafe { GetAsyncKeyState(i32::from(key)) } < 0 {
            return Err(denied("release held modifiers before injected input"));
        }
    }
    Ok(())
}

fn insert(events: &[INPUT]) -> usize {
    // SAFETY: bounded array of fully initialized keyboard INPUTs, exact ABI
    // size and live pointer. Surrogate pairs stay in the same call. One batch
    // cannot interleave with other injected/user input events between its keys.
    (unsafe {
        SendInput(
            events.len() as u32,
            events.as_ptr(),
            size_of::<INPUT>() as i32,
        )
    }) as usize
}

fn key_code(key: &str) -> io::Result<u16> {
    if key.is_empty() || key.len() > 12 {
        return Err(denied("unsupported key"));
    }
    let normalized = key.to_ascii_lowercase();
    Ok(match normalized.as_str() {
        "enter" => VK_RETURN,
        "escape" => VK_ESCAPE,
        "tab" => VK_TAB,
        "backspace" => VK_BACK,
        "delete" => VK_DELETE,
        "space" => VK_SPACE,
        "up" => VK_UP,
        "down" => VK_DOWN,
        "left" => VK_LEFT,
        "right" => VK_RIGHT,
        "home" => VK_HOME,
        "end" => VK_END,
        "page_up" => VK_PRIOR,
        "page_down" => VK_NEXT,
        "f1" => VK_F1,
        "f2" => VK_F2,
        "f3" => VK_F3,
        "f4" => VK_F4,
        "f5" => VK_F5,
        "f6" => VK_F6,
        "f7" => VK_F7,
        "f8" => VK_F8,
        "f9" => VK_F9,
        "f10" => VK_F10,
        "f11" => VK_F11,
        "f12" => VK_F12,
        "control" => VK_CONTROL,
        "alt" => VK_MENU,
        "shift" => VK_SHIFT,
        "meta" => VK_LWIN,
        _ if normalized.len() == 1 && normalized.as_bytes()[0].is_ascii_alphanumeric() => {
            u16::from(normalized.as_bytes()[0].to_ascii_uppercase())
        }
        _ => return Err(denied("unsupported key")),
    })
}

fn virtual_event(key: u16, release: bool) -> INPUT {
    let extended = matches!(
        key,
        VK_DELETE
            | VK_UP
            | VK_DOWN
            | VK_LEFT
            | VK_RIGHT
            | VK_HOME
            | VK_END
            | VK_PRIOR
            | VK_NEXT
            | VK_LWIN
            | VK_RWIN
    );
    INPUT {
        r#type: INPUT_KEYBOARD,
        Anonymous: INPUT_0 {
            ki: KEYBDINPUT {
                wVk: key,
                wScan: 0,
                dwFlags: if release { KEYEVENTF_KEYUP } else { 0 }
                    | if extended { KEYEVENTF_EXTENDEDKEY } else { 0 },
                time: 0,
                dwExtraInfo: 0,
            },
        },
    }
}

fn chord_inputs(key: &str, modifiers: &[String]) -> io::Result<Vec<INPUT>> {
    if modifiers.len() > 4 {
        return Err(denied("unsupported modifiers"));
    }
    let key = key_code(key)?;
    if ((key as u8).is_ascii_uppercase() || (key as u8).is_ascii_digit()) && modifiers.is_empty() {
        return Err(denied("text is not a bare key chord"));
    }
    let mut held = Vec::new();
    for modifier in modifiers {
        let code = key_code(modifier)?;
        if !matches!(code, VK_CONTROL | VK_MENU | VK_SHIFT | VK_LWIN)
            || held.contains(&code)
            || code == key
        {
            return Err(denied("unsupported or duplicate modifier"));
        }
        held.push(code);
    }
    let mut events: Vec<_> = held.iter().map(|key| virtual_event(*key, false)).collect();
    events.extend([virtual_event(key, false), virtual_event(key, true)]);
    events.extend(held.iter().rev().map(|key| virtual_event(*key, true)));
    Ok(events)
}

fn partial_chord_releases(events: &[INPUT], inserted: usize) -> Vec<INPUT> {
    let mut down = Vec::new();
    for event in events.iter().take(inserted) {
        // SAFETY: only chord_inputs' fully initialized keyboard events arrive.
        let key = unsafe { event.Anonymous.ki };
        if key.dwFlags & KEYEVENTF_KEYUP != 0 {
            down.retain(|value| *value != key.wVk);
        } else {
            down.push(key.wVk);
        }
    }
    down.iter()
        .rev()
        .map(|key| virtual_event(*key, true))
        .collect()
}

/// Complete chord in one input-stream insertion, independent of the worker's
/// keyboard layout. On partial insertion only release this chord's own keys;
/// never repeat its key-down/action or release keys that were already held.
pub fn send_key_chord(key: &str, modifiers: &[String]) -> io::Result<()> {
    let events = chord_inputs(key, modifiers)?;
    super::desktop::ensure_input_desktop()?;
    require_released_modifiers()?;
    // SAFETY: value-only query for the validated base key in the current desktop.
    if unsafe { GetAsyncKeyState(i32::from(key_code(key)?)) } < 0 {
        return Err(denied("release held key before injected input"));
    }
    let inserted = insert(&events);
    if inserted != events.len() {
        let releases = partial_chord_releases(&events, inserted);
        if !releases.is_empty() {
            let _ = insert(&releases);
        }
        return Err(io::Error::other("Windows key chord was not fully inserted"));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn literal_unicode_has_no_virtual_keys_or_shortcut_syntax() {
        let text = "VCW-{ctrl}中文🙂";
        let events = unicode_inputs(text).unwrap();
        let units: Vec<_> = text.encode_utf16().collect();
        assert_eq!(events.len(), units.len() * 2);
        for (pair, unit) in events.chunks_exact(2).zip(units) {
            for (index, event) in pair.iter().enumerate() {
                assert_eq!(event.r#type, INPUT_KEYBOARD);
                // SAFETY: unicode_inputs initialized exactly the keyboard union.
                let key = unsafe { event.Anonymous.ki };
                assert_eq!(key.wVk, 0);
                assert_eq!(key.wScan, unit);
                assert_eq!(
                    key.dwFlags,
                    KEYEVENTF_UNICODE | if index == 1 { KEYEVENTF_KEYUP } else { 0 }
                );
                assert_eq!(key.time, 0);
                assert_eq!(key.dwExtraInfo, 0);
            }
        }
    }

    #[test]
    fn text_chunks_are_bounded_before_any_os_input() {
        for text in [
            String::new(),
            "a\0b".into(),
            "a".repeat(33),
            "🙂".repeat(33),
        ] {
            assert!(unicode_inputs(&text).is_err());
        }
        assert_eq!(unicode_inputs(&"🙂".repeat(32)).unwrap().len(), 128);
        assert_eq!(unicode_inputs(&"a".repeat(32)).unwrap().len(), 64);
    }

    #[test]
    fn system_context_cannot_inject_keyboard_input() {
        if current_identity().unwrap().is_system() {
            let error = send_unicode_text_chunk("public fixture").unwrap_err();
            assert_eq!(error.kind(), io::ErrorKind::PermissionDenied);
            assert_eq!(
                send_key_chord("a", &["control".into()]).unwrap_err().kind(),
                io::ErrorKind::PermissionDenied
            );
        }
    }

    #[test]
    fn chords_are_virtual_keys_in_one_balanced_batch() {
        let events = chord_inputs("a", &["control".into(), "shift".into()]).unwrap();
        let want = [
            (VK_CONTROL, false),
            (VK_SHIFT, false),
            (0x41, false),
            (0x41, true),
            (VK_SHIFT, true),
            (VK_CONTROL, true),
        ];
        assert_eq!(events.len(), want.len());
        for (event, (code, release)) in events.iter().zip(want) {
            assert_eq!(event.r#type, INPUT_KEYBOARD);
            // SAFETY: chord_inputs initializes the keyboard union.
            let key = unsafe { event.Anonymous.ki };
            assert_eq!(key.wVk, code);
            assert_eq!(key.wScan, 0);
            assert_eq!(key.dwFlags, if release { KEYEVENTF_KEYUP } else { 0 });
        }
        let events = chord_inputs("left", &[]).unwrap();
        // SAFETY: chord_inputs initializes the keyboard union.
        assert_eq!(
            unsafe { events[0].Anonymous.ki.dwFlags },
            KEYEVENTF_EXTENDEDKEY
        );
    }

    #[test]
    fn invalid_chords_cannot_expand_into_input_expressions() {
        for (key, modifiers) in [
            ("{ctrl}a", vec![]),
            ("a", vec![]),
            ("🙂", vec!["control".into()]),
            ("a", vec!["alt".into(), "ALT".into()]),
            ("control", vec!["control".into()]),
            ("a", vec!["enter".into()]),
            ("a", vec!["ctrl".into()]),
        ] {
            assert!(chord_inputs(key, &modifiers).is_err());
        }
        for key in ["enter", "tab", "f12", "delete", "meta", "space", "page_up"] {
            assert_eq!(chord_inputs(key, &[]).unwrap().len(), 2);
        }
    }

    #[test]
    fn partial_chord_only_releases_its_inserted_down_keys() {
        let events = chord_inputs("a", &["control".into()]).unwrap();
        for (inserted, expected) in [
            (0, vec![]),
            (1, vec![VK_CONTROL]),
            (2, vec![0x41, VK_CONTROL]),
            (3, vec![VK_CONTROL]),
            (4, vec![]),
        ] {
            let releases = partial_chord_releases(&events, inserted);
            let codes: Vec<_> = releases
                .iter()
                .map(|event| {
                    // SAFETY: release events are initialized keyboard inputs.
                    let key = unsafe { event.Anonymous.ki };
                    assert_ne!(key.dwFlags & KEYEVENTF_KEYUP, 0);
                    key.wVk
                })
                .collect();
            assert_eq!(codes, expected);
        }
    }
}
