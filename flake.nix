{
  description = "Compact preservation of Redumper .scram CD-ROM dumps";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};

        version =
          if (self ? rev) then
            "git-${builtins.substring 0 7 self.rev}"
          else
            "git-dirty";

        # Filter the source so a local `nix build` doesn't slurp test
        # fixtures (multi-GB) or build artefacts into the nix store.
        # When fetched via the flake URL (a tagged release tarball)
        # gitignored files are absent anyway.
        src = pkgs.lib.cleanSourceWith {
          name = "miniscram-source";
          src = ./.;
          filter = path: type:
            let baseName = baseNameOf (toString path);
            in
              baseName != ".git"
              && baseName != ".github"
              && baseName != "test-discs"
              && baseName != "miniscram"
              && baseName != "dist"
              && baseName != "bin"
              && !(pkgs.lib.hasSuffix ".pdf" baseName)
              && !(pkgs.lib.hasSuffix ".out" baseName)
              && !(pkgs.lib.hasSuffix ".test" baseName);
        };
        # Native deps Gio v0.9 needs at build + run time on Linux.
        # X11 stack + Wayland + GL + Vulkan; pkg-config and a C toolchain
        # are provided via stdenv.
        guiNativeBuildInputs = with pkgs; [ pkg-config ];
        guiBuildInputs = with pkgs; [
          libGL
          libxkbcommon
          wayland
          vulkan-headers
          vulkan-loader
          libxcb
          libx11
          libxcursor
          libxfixes
          libxi
          libxinerama
          libxrandr
          libxxf86vm
        ];

      in {
        packages.default = pkgs.buildGoModule {
          pname = "miniscram";
          inherit version src;

          # The repo has nested submodules (scripts/sweep, tools/miniscram-gui)
          # with their own go.mod. Restrict to the root package so buildGoModule
          # doesn't try to build the submodules from the wrong module context.
          subPackages = [ "." ];

          # No third-party dependencies; the module graph is stdlib-only.
          vendorHash = null;

          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
          ];

          # The build-tagged e2e tests (-tags redump_data) need real
          # Redumper dumps that aren't shipped. The default test run is
          # a fast, hermetic suite that's safe to enforce in the build.
          doCheck = true;

          meta = with pkgs.lib; {
            description = "Compact preservation of Redumper .scram CD-ROM dumps";
            homepage = "https://github.com/hughobrien/miniscram";
            license = licenses.gpl3Only;
            mainProgram = "miniscram";
            platforms = platforms.unix ++ [ "x86_64-windows" ];
          };
        };

        # The desktop wrapper. Has its own go.mod and a real third-party
        # graph (Gio + modernc.org/sqlite), so vendorHash is a real hash
        # rather than null. cgo is required for Gio's GL bindings.
        packages.miniscram-gui = pkgs.buildGoModule {
          pname = "miniscram-gui";
          inherit version;
          src = ./tools/miniscram-gui;
          vendorHash = "sha256-6XYri4ATQf9esa+HvJEHPY9apV+iRoe/TlqBmY5SB9o=";

          nativeBuildInputs = guiNativeBuildInputs;
          buildInputs = guiBuildInputs;

          # cgo is required for Gio's GL/Vulkan bindings.
          env.CGO_ENABLED = "1";

          # Tests are run by GitHub CI in a real Ubuntu environment with all
          # the apt deps present. In the nix sandbox, linking the cgo-heavy
          # test binary exhausts the build /tmp; cheaper to skip the tests
          # here and trust CI.
          doCheck = false;

          ldflags = [
            "-s"
            "-w"
          ];

          meta = with pkgs.lib; {
            description = "Desktop wrapper around the miniscram CLI (Gio)";
            homepage = "https://github.com/hughobrien/miniscram";
            license = licenses.gpl3Only;
            mainProgram = "miniscram-gui";
            platforms = platforms.linux;
          };
        };

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            gotools
          ];
        };

        # devShells.gui is the same Go toolchain plus everything Gio
        # needs to compile + link. Useful for `go build` / `go test` of
        # the GUI submodule outside `nix build`.
        devShells.gui = pkgs.mkShell {
          nativeBuildInputs = guiNativeBuildInputs;
          buildInputs = guiBuildInputs;
          packages = with pkgs; [ go gopls gotools ];
        };

        formatter = pkgs.nixfmt-rfc-style;
      });
}
