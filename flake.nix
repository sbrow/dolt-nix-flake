/* WARNING: This file is generated by main.go in github.com/dolthub/dolt-nix-flake. */
{
  description = "Relational database with version control and CLI a-la Git";

  inputs = {
    flake-utils.url = "github:numtide/flake-utils";
    dolt.url = "github:dolthub/dolt";
    dolt.flake = false;
  };

  outputs = { self, dolt, flake-utils, nixpkgs }: flake-utils.lib.eachDefaultSystem (system:
    let
      pkgs = nixpkgs.legacyPackages.${system};
      lib = nixpkgs.lib;
    in
    {
      packages.default = pkgs.buildGoModule {
        name = "dolt";

        /* Based on https://github.com/NixOS/nixpkgs/blob/master/pkgs/servers/sql/dolt/default.nix */
        pname = "dolt";

        src = dolt;
        modRoot = "./go";
        subPackages = [ "cmd/dolt" ];
        vendorHash = "sha256-jrHrv08mwjq4a8gDlrhUe+A7qMFzcdhW/cZFQPcAQ94=";
        proxyVendor = true;
        doCheck = false;

        meta = with lib; {
          description = "Relational database with version control and CLI a-la Git";
          homepage = "https://github.com/dolthub/dolt";
          license = licenses.asl20;
          maintainers = with maintainers; [ danbst ];
        };
      };
    });
}
