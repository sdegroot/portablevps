# Adds the local QEMU test SSH key for noninteractive disaster recovery validation.
{ ... }:

{
  users.users.admin.openssh.authorizedKeys.keyFiles = [
    ../../keys/qemu-test.pub
  ];
}
