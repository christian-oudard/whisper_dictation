Name:           diktat
Version:        1.0.0
Release:        1%{?dist}
Summary:        Voice dictation that types where you are looking

License:        MIT
URL:            https://github.com/christian-oudard/diktat
Source0:        %{url}/archive/refs/tags/v%{version}.tar.gz#/%{name}-%{version}.tar.gz
# The speech library is not packaged anywhere, so it is built from source here
# and linked statically. Source1 is a checkout of it at the revision this
# release was tested against.
Source1:        transcribe.cpp-%{version}.tar.gz

BuildRequires:  golang >= 1.22
BuildRequires:  cmake
BuildRequires:  gcc-c++
BuildRequires:  git
BuildRequires:  vulkan-loader-devel
BuildRequires:  vulkan-headers
BuildRequires:  glslang
BuildRequires:  alsa-lib-devel
BuildRequires:  pipewire-devel

Requires:       espeak-ng
Requires:       ffmpeg
Requires:       vulkan-loader

# Which one a user needs depends on their session: wtype for the wlroots
# compositors, ydotool for GNOME and KDE, xdotool for X11. Any of the three is
# enough and diktat says which are missing when it cannot type, so none is
# required. The clipboard the paste methods borrow follows the same rule:
# wl-clipboard on Wayland, xclip or xsel on X11.
Recommends:     wtype
Recommends:     wl-clipboard
Suggests:       ydotool
Suggests:       xdotool
Suggests:       xclip

%description
Press a key, speak, press it again, and the words are typed into whatever
window has focus. The daemon holds one speech model for the whole session, so a
dictation costs the time it takes to say it rather than the time it takes to
load a model.

Speech recognition runs locally on the CPU or a Vulkan GPU. No audio leaves the
machine and nothing needs a network once a model has been fetched; models are
downloaded on request into the user's cache, never at install time.

It also transcribes recordings into documents that say who spoke, by clustering
speaker embeddings rather than by a model with a fixed number of speakers built
into it.

%prep
%setup -q
%setup -q -T -D -a 1

%build
# One recipe, shared with the PKGBUILD and debian/rules: it reads the speech
# library's own link manifest to find which archives and system libraries the
# enabled backends need.
TRANSCRIBE_SRC=%{_builddir}/%{name}-%{version}/transcribe.cpp \
BUILD_DIR=%{_builddir}/transcribe-build \
VERSION=%{version} \
DIKTAT_REV=v%{version} \
OUT=%{_builddir}/diktat \
    sh packaging/build.sh

%check
# Everything that needs no model, microphone or compositor.
go test ./internal/...

%install
install -Dpm 0755 %{_builddir}/diktat %{buildroot}%{_bindir}/diktat
install -Dpm 0644 packaging/diktat.service %{buildroot}%{_userunitdir}/diktat.service
install -Dpm 0644 completions/_diktat %{buildroot}%{_datadir}/zsh/site-functions/_diktat
install -Dpm 0644 packaging/diktat.1 %{buildroot}%{_mandir}/man1/diktat.1

%files
%license LICENSE
%doc README.md
%{_bindir}/diktat
%{_userunitdir}/diktat.service
%{_datadir}/zsh/site-functions/_diktat
%{_mandir}/man1/diktat.1*

%changelog
* Sun Aug 23 2026 Diktat maintainers <noreply@example.com> - 1.0.0-1
- First release.
