{
  description = "Standalone ports of xCAT pping, xdsh and xdcp (no xcatd, no xCAT DB)";

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
          postInstall = ''
            if [ -f "man/man1/${name}.1" ]; then
              install -Dm644 "man/man1/${name}.1" "$out/share/man/man1/${name}.1"
            fi
          '';
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
          xdcp = buildXcatTool { name = "xdcp"; mainpkg = "xdcp"; };
          default = pkgs.symlinkJoin {
            name = "xcat-ports-${version}";
            paths = [ self.packages.${system}.pping self.packages.${system}.xdsh self.packages.${system}.xdcp ];
          };
        };

        # pping fronts fping/nmap, xdsh fronts ssh/scp, xdcp fronts rsync/scp - provide them for shells
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [ go fping nmap openssh rsync ];
        };

        apps = {
          pping = flake-utils.lib.mkApp { drv = self.packages.${system}.pping; };
          xdsh = flake-utils.lib.mkApp { drv = self.packages.${system}.xdsh; };
          xdcp = flake-utils.lib.mkApp { drv = self.packages.${system}.xdcp; };
        };
      });
}
