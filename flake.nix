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
      in {
        packages.default = pkgs.buildGoModule {
          pname = "miniscram";
          inherit version src;

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

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            gotools
          ];
        };

        formatter = pkgs.nixfmt-rfc-style;
      });
}
