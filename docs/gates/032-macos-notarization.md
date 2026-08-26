# Task 032 gate — macOS signing and notarization

**Acceptance (task 032.7):** a real user downloading a real release on a real
Mac sees **no** Gatekeeper dialog, and the `.pkg` installs with the network off.

This gate has **no script**, deliberately, for the same reason
`scripts/m3-gate.sh` seeds instead of asserting and
[`017`](017-workflow-graph.md) has no script at all: what is being judged is
what *macOS* does with a file that arrived over the network, and CI cannot
produce that file. The release job already asserts everything a machine can —
`codesign --verify --strict`, the hardened-runtime flag, `notarytool` exiting
non-zero on rejection, `stapler validate`, `pkgutil --check-signature`, `spctl
--assess --type install` — and the macOS smoke leg writes a *synthetic*
`com.apple.quarantine` attribute because `actions/download-artifact` does not
carry extended attributes. A synthetic attribute is a strong proxy for the
first-launch dialog. It is not the dialog.

Three things only a human on a clean machine can settle, and they are the three
below.

## Prerequisites

- A Mac the release has **never** been installed on and that has never had
  vincent's Developer ID certificate in a keychain. A machine that has built
  vincent locally is not clean: a locally built binary is ad-hoc signed and is
  never quarantined, so it proves nothing about the released one.
- A published stable tag whose `Release` workflow went green **with the six
  Apple secrets configured**. A dry run cannot be used here: it is designed to
  produce unsigned artifacts when secrets are absent, so a green dry run is not
  evidence of a working signature.
- A browser. `curl` and `gh release download` do **not** set
  `com.apple.quarantine`; Safari and Chrome do, and the quarantine attribute is
  what makes Gatekeeper assess the file at all. Downloading any other way
  silently tests nothing.

## 1. The archive

Download `vincent_X.Y.Z_darwin_arm64.tar.gz` (or `_amd64` on Intel) **in the
browser**, from the release page. Then, in Terminal:

```sh
cd ~/Downloads
xattr -p com.apple.quarantine vincent_*_darwin_arm64.tar.gz   # must print a value
tar -xzf vincent_*_darwin_arm64.tar.gz
xattr -p com.apple.quarantine vincent                          # inherited by the payload
./vincent version
```

**Look for:** the version prints. **No dialog appears** — not "cannot be opened
because it is from an unidentified developer", not "Apple could not verify…",
and nothing that has to be dismissed in *System Settings → Privacy & Security*.

If a dialog does appear, the gate has failed; do not clear the attribute to make
it go away. Then confirm what Gatekeeper decided:

```sh
codesign --verify --strict --verbose=2 ./vincent
spctl --assess --type execute -vv ./vincent
```

`spctl` must print `accepted` and `source=Notarized Developer ID`. `source=No
Rules` or `source=Unnotarized Developer ID` is a failure even if the binary ran.

## 2. The `.pkg`, offline

This is the only leg that distinguishes the `.pkg` from the archive: its
notarization ticket is **stapled** into the file, so it needs no network. Verify
that by removing the network.

Download `vincent_X.Y.Z_darwin_universal.pkg` in the browser, then:

```sh
pkgutil --check-signature vincent_*_darwin_universal.pkg
spctl --assess --type install -vv vincent_*_darwin_universal.pkg
xcrun stapler validate vincent_*_darwin_universal.pkg
```

Now **turn Wi-Fi off and unplug Ethernet**, and only then:

```sh
sudo installer -pkg vincent_*_darwin_universal.pkg -target /
/usr/local/bin/vincent version
lipo -info /usr/local/bin/vincent        # x86_64 arm64
```

**Look for:** the install completes and the binary runs with no network and no
dialog. Double-clicking the `.pkg` in Finder must open Installer's normal
sheet — the one naming the developer — not a refusal.

Repeat the *first* command of this section with the network still off; `stapler
validate` succeeding offline is the whole claim.

## 3. The cask, with its quarantine hook gone

Task 032 removed `hooks.post.install`'s `xattr -dr com.apple.quarantine` from
the cask. That hook was load-bearing before notarization, so this leg exists to
prove it no longer is.

```sh
brew install lezli01/tap/vincent
vincent version
binary="$(readlink -f "$(brew --prefix)/bin/vincent")"
spctl --assess --type execute -vv "$binary"
```

**Look for:** `brew install` completes and `vincent version` runs. `spctl`
reports `accepted`, `source=Notarized Developer ID`.

**Do not look for** the absence of `com.apple.quarantine` — that was the old
criterion and it is now the wrong one. The attribute may well be present; what
matters is that Gatekeeper accepts the file anyway, which is the difference
between clearing the check and passing it.

## Also confirm nothing moved on Windows

Task 032 is macOS-only by decision, so the gate's last question is that nothing
quietly changed elsewhere: on Windows, the SmartScreen prompt should still
appear on first launch of a downloaded `vincent.exe`, and the documentation
should still say so. A release that silently stopped prompting would mean
something unintended happened, not an improvement.

## Runs

| Date | Version | macOS | Walked by | Result |
|---|---|---|---|---|
| — | — | — | — | not yet walked; blocked on 032.7 (Apple Developer Program enrolment and the six repository secrets) |
