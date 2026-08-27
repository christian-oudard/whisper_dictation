{
  description = "Voice dictation with transcribe.cpp";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  # The speech library the daemon links against, which packages itself. The
  # fork, until the Go bindings and the flake land upstream; after that this
  # becomes github:handy-computer/transcribe.cpp and nothing else changes.
  inputs.transcribe-cpp.url = "github:christian-oudard/transcribe.cpp";
  inputs.transcribe-cpp.inputs.nixpkgs.follows = "nixpkgs";

  outputs =
    { self, nixpkgs, transcribe-cpp }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};

      # External CLIs the daemon shells out to.
      runtimeDeps = [
        pkgs.wtype
        # The warmup rehearses on synthesised speech; see warm() in daemon.go.
        pkgs.espeak-ng
        pkgs.wl-clipboard
        pkgs.sway
        # `diktat transcribe` takes a recording in whatever format the thing
        # that made it writes, and converts anything that is not already a
        # 16 kHz WAV; see internal/wav/decode.go.
        pkgs.ffmpeg
      ];
      runtimeBin = pkgs.lib.makeBinPath runtimeDeps;

      # One library for every family, whisper and moonshine alike. The
      # encoder runs on a padded 30 second window for whisper whatever the
      # utterance length, so it dominates transcription time: ~510ms on 22 CPU
      # threads against ~9ms on a laptop RTX 4070, hence the Vulkan build.
      transcribe = transcribe-cpp.packages.${system}.default;

      # miniaudio dlopens these by SONAME at runtime.
      audioInputs = [
        pkgs.alsa-lib
        pkgs.libpulseaudio
        pkgs.pipewire
      ];
      audioLibs = pkgs.lib.makeLibraryPath audioInputs;

      # The store hash says whether two builds differ, not which commit they
      # came from, so stamp the revision in. A dirty tree has no rev, only
      # dirtyShortRev, and a tarball source has neither.
      gitRev = self.shortRev or self.dirtyShortRev or "unknown";
      # lastModifiedDate is YYYYMMDDHHMMSS; show it the way a person reads it.
      gitDate =
        let
          d = toString (self.lastModifiedDate or "");
          at = n: len: builtins.substring n len d;
        in
        # RFC3339, which has no spaces: ldflags are joined on spaces, so one
        # here would split the flag. lastModifiedDate is UTC, hence the Z; the
        # binary converts to local time when printing.
        if builtins.stringLength d == 14 then
          "${at 0 4}-${at 4 2}-${at 6 2}T${at 8 2}:${at 10 2}:${at 12 2}Z"
        else
          "";
    in
    {
      packages.${system}.default = pkgs.buildGoModule {
        pname = "diktat";
        version = "1.0.0";
        src = ./.;
        vendorHash = null;
        ldflags = [
          # What the release is called, and what it was built from. A package
          # has both; a build from a working tree has only the second.
          "-X main.version=1.0.0"
          "-X main.gitRev=${gitRev}"
          "-X main.gitDate=${gitDate}"
        ];
        # libtranscribe is linked in, not shelled out to, so the model stays
        # loaded between utterances.
        buildInputs = [ transcribe ];
        subPackages = [ "cmd/diktat" ];
        # The default check phase tests only subPackages, which is cmd/diktat
        # and has no test files, so it ran nothing. Test everything instead.
        #
        # It also drops -trimpath for tests, in case they read assets by path.
        # That changes the build cache key, so every dependency buildPhase just
        # compiled is compiled again, and internal/audio binds miniaudio, a
        # single 96k-line header worth 17s on its own. Nothing here reads an
        # asset, so keep the flag and reuse the cache.
        checkPhase = ''
          runHook preCheck
          go test ./...
          runHook postCheck
        '';
        nativeBuildInputs = [ pkgs.makeWrapper ];
        postInstall = ''
          for bin in $out/bin/*; do
            wrapProgram "$bin" \
              --prefix PATH : ${runtimeBin} \
              --suffix LD_LIBRARY_PATH : ${audioLibs}:${pkgs.vulkan-loader}/lib
          done
          install -Dm644 $src/completions/_diktat $out/share/zsh/site-functions/_diktat
        '';
      };

      # The daemon as a home-manager service, since a session-long process with
      # a store path in it is not something to hand-write per machine. Nothing
      # about the unit is host-specific: the daemon finds the compositor
      # itself, so it neither waits for a session nor takes anything from one,
      # and the compositor config is left holding only its keybindings.
      homeManagerModules.default = {
        home.packages = [ self.packages.${system}.default ];

        systemd.user.services.diktat = {
          Unit = {
            Description = "diktat dictation daemon";
            # The daemon types into a graphical session and holds a model for
            # as long as one lasts, so its lifetime is the session's: PartOf
            # stops it when the session goes. Not default.target, which the
            # user manager reaches on any login, including one over ssh, where
            # this would load a model and wait for a compositor that is never
            # coming.
            PartOf = [ "graphical-session.target" ];
          };
          # Every session a display manager starts reaches this target, on X11
          # and on Wayland alike, so this one line covers GNOME, KDE and the
          # rest. A compositor started from a tty does not reach it, and starts
          # the service by name instead; see the README.
          Install.WantedBy = [ "graphical-session.target" ];
          Service = {
            ExecStart = "${self.packages.${system}.default}/bin/diktat daemon";
            # It never exits on its own, so an exit is a failure. The store
            # path in ExecStart makes an upgrade a change to this unit, which
            # is what gets the new build restarted at activation.
            Restart = "on-failure";
            RestartSec = 2;
            # EX_CONFIG. A config file only a person can fix, so restarting
            # would just bury the reason in the journal.
            RestartPreventExitStatus = 78;
          };
        };
      };

      # `go run`/`go build`/`go test` with the same runtime env the wrapper
      # sets, so the binaries work without going through `nix build`.
      devShells.${system}.default = pkgs.mkShell {
        packages = [ pkgs.go ] ++ runtimeDeps;
        buildInputs = audioInputs ++ [ transcribe ];
        LD_LIBRARY_PATH = "${audioLibs}:${pkgs.vulkan-loader}/lib";
      };
    };
}
