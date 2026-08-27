# Releasing

What a release is, in order. Everything here is a thing to check rather than a
thing to run: there is no release script, because the parts that could be
automated are the parts that do not go wrong.

## Before the tag

1. `nix develop -c go test ./...` and `nix build`. The unit tests need no
   model, microphone or compositor; nothing in them will catch a bad model.
2. Dictate something. The tests cannot: press the key, say a sentence, watch it
   appear. This is the only check that the whole chain works, and it has caught
   what unit tests cannot -- a compositor that stopped answering, a microphone
   that went silent, a model that loaded and returned nothing.
3. Transcribe a recording with more than one person in it, and read the top of
   the transcript. Speaker labels are wrong in ways that no assertion catches
   and any reader sees immediately.
4. Bump the version everywhere it is written down: `flake.nix` (`version` and
   the `-X main.version` ldflag), `packaging/aur/PKGBUILD` (`pkgver`),
   `packaging/debian/changelog` (a new entry), and
   `packaging/rpm/diktat.spec` (`Version`), and a heading in `CHANGELOG.md`
   saying what changed. `go test ./cmd/diktat/` fails when
   they disagree, which is why step 1 comes before this one and again after
   it.
5. Fill in who is publishing. `packaging/debian/control`,
   `packaging/debian/changelog` and the `%changelog` entry in
   `packaging/rpm/diktat.spec` carry a placeholder maintainer, and both
   archives reject or complain about a package that is uploaded with one. The
   PKGBUILD names the repository instead, which is what the AUR expects.
6. Check `packaging/build.sh` still builds against the library version you are
   about to release with, since it is the recipe both distributions use:
   `TRANSCRIBE_SRC=../transcribe.cpp VERSION=x.y.z sh packaging/build.sh`,
   then run `./diktat version` and confirm the number.

## The speech library

diktat links `libtranscribe` statically, so a release pins a version of it.
Both the flake input and the PKGBUILD name a revision, and they have to name
the same one. A release whose flake and PKGBUILD disagree produces two binaries
that behave differently and report the same version, which is worse than a
build failure.

## After the tag

- **Nix** needs nothing: the flake input is the tag.
- **Arch**: update `pkgver` and `sha256sums` in the AUR repository's PKGBUILD,
  then `makepkg --printsrcinfo > .SRCINFO` and push. `SKIP` is fine for the git
  source and is not fine for the release tarball, which has a hash worth
  checking.
- **Debian**: `dpkg-buildpackage -b -uc -us` from a tree with `packaging/debian`
  copied to `debian/`. This targets a PPA or a local `.deb`; Debian proper
  wants `libtranscribe` packaged separately first, which is a different piece
  of work.
- **Fedora and openSUSE**: `rpmbuild -ba packaging/rpm/diktat.spec` with both
  tarballs in `SOURCES`. The second is the speech library at the revision this
  release was tested against, which the spec expects to be named for the diktat
  version rather than its own.

## What a release does not promise

The model menu is not versioned with the binary. Models are fetched from
upstream repositories at run time, and an entry can stop resolving between
releases without anything here changing. The menu says what is downloaded, so
the failure is visible, but a release cannot pin it.

Diarization reports a speaker count it estimated, and the estimate runs one
over on three of the four meetings it has been measured against. See
`docs/diarization.md` in transcribe.cpp for the numbers. It is worth saying in
release notes, because a user who reads "five speakers" for a four-person
meeting should know that is the known shape of the error rather than a mystery.
