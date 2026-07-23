# Enables Docker for roles that need Docker-native tooling.
{ ... }:

{
  virtualisation.docker = {
    enable = true;
    autoPrune = {
      enable = true;
      dates = "weekly";
    };
  };
}
