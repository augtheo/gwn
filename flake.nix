{
  description = "gwn — workspace navigator TUI";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in {
        packages.default = pkgs.buildGoModule {
          pname = "gwn";
          version = "0.1.0";
          src = ./.;
          vendorHash = null;
          meta = {
            description = "Git worktree/workspace navigator TUI";
            license = pkgs.lib.licenses.mit;
            mainProgram = "gwn";
          };
        };

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            gopls
            golangci-lint
            gotools
            git
          ];
        };

        apps.default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/gwn";
        };
      });
}
