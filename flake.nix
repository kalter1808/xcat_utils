{
  description = "Standalone ports of xCAT pping and xdsh (no xcatd, no xCAT DB)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        version = "0.1.0";
        src = ./.;
        buildXcatTool = { name, mainpkg }: pkgs.buildGoModule {
          pname = name;
          inherit version src;
          subPackages = [ "cmd/${mainpkg}" ];
          vendorHash = null;
          meta = with pkgs.lib; {
            description = "${name} - standalone port of xCAT ${name} (local noderange expansion, no xcatd)";
            license = licenses.epl10;
            mainProgram = name;
          };
        };
      in {
        packages = {
          pping = buildXcatTool { name = "pping"; mainpkg = "pping"; };
          xdsh = buildXcatTool { name = "xdsh"; mainpkg = "xdsh"; };
          default = pkgs.symlinkJoin {
            name = "xcat-ports-${version}";
            paths = [ self.packages.${system}.pping self.packages.${system}.xdsh ];
          };
        };

        # pping fronts fping/nmap, xdsh fronts ssh/scp - provide them for shells
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [ go fping nmap openssh ];
        };

        apps = {
          pping = flake-utils.lib.mkApp { drv = self.packages.${system}.pping; };
          xdsh = flake-utils.lib.mkApp { drv = self.packages.${system}.xdsh; };
        };
      });
}
