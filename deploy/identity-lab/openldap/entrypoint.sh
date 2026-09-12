#!/bin/sh
set -eu

: "${LDAP_ADMIN_PASSWORD:?LDAP_ADMIN_PASSWORD is required}"
: "${LDAP_ALICE_PASSWORD:?LDAP_ALICE_PASSWORD is required}"
: "${LDAP_BOB_PASSWORD:?LDAP_BOB_PASSWORD is required}"

state_dir=/var/lib/vc-workspace-lab-ldap
database_dir="$state_dir/database"
config_file="$state_dir/slapd.conf"
data_file="$state_dir/directory.ldif"
password_file="$state_dir/password"

mkdir -p "$state_dir" "$database_dir" /shared
chmod 0700 "$state_dir" "$database_dir"

hash_password() {
  umask 077
  printf '%s' "$1" >"$password_file"
  slappasswd -T "$password_file"
  rm -f "$password_file"
}

admin_hash=$(hash_password "$LDAP_ADMIN_PASSWORD")
alice_hash=$(hash_password "$LDAP_ALICE_PASSWORD")
bob_hash=$(hash_password "$LDAP_BOB_PASSWORD")

openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
  -subj '/CN=VC Workspace identity lab CA' \
  -keyout "$state_dir/ca.key" -out /shared/ca.crt >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj '/CN=ldap' \
  -addext 'subjectAltName=DNS:ldap,DNS:localhost,IP:127.0.0.1' \
  -keyout "$state_dir/server.key" -out "$state_dir/server.csr" >/dev/null 2>&1
openssl x509 -req -days 2 -sha256 \
  -in "$state_dir/server.csr" -CA /shared/ca.crt -CAkey "$state_dir/ca.key" -CAcreateserial \
  -copy_extensions copy -out "$state_dir/server.crt" >/dev/null 2>&1

cat >"$config_file" <<EOF
include /etc/ldap/schema/core.schema
include /etc/ldap/schema/cosine.schema
include /etc/ldap/schema/nis.schema
include /etc/ldap/schema/inetorgperson.schema

pidfile $state_dir/slapd.pid
argsfile $state_dir/slapd.args
modulepath /usr/lib/ldap
moduleload back_mdb

TLSCACertificateFile /shared/ca.crt
TLSCertificateFile $state_dir/server.crt
TLSCertificateKeyFile $state_dir/server.key
TLSProtocolMin 3.3

database mdb
maxsize 1073741824
suffix "dc=lab,dc=vc-workspace,dc=test"
rootdn "cn=admin,dc=lab,dc=vc-workspace,dc=test"
rootpw $admin_hash
directory $database_dir
index objectClass eq
index uid,uidNumber,gidNumber eq

access to attrs=userPassword
  by self write
  by anonymous auth
  by dn.exact="cn=admin,dc=lab,dc=vc-workspace,dc=test" manage
  by * none
access to *
  by self read
  by users read
  by anonymous read
EOF

cat >"$data_file" <<EOF
dn: dc=lab,dc=vc-workspace,dc=test
objectClass: top
objectClass: dcObject
objectClass: organization
dc: lab
o: VC Workspace identity lab

dn: ou=people,dc=lab,dc=vc-workspace,dc=test
objectClass: top
objectClass: organizationalUnit
ou: people

dn: ou=groups,dc=lab,dc=vc-workspace,dc=test
objectClass: top
objectClass: organizationalUnit
ou: groups

dn: uid=alice,ou=people,dc=lab,dc=vc-workspace,dc=test
objectClass: top
objectClass: person
objectClass: organizationalPerson
objectClass: inetOrgPerson
objectClass: posixAccount
objectClass: shadowAccount
uid: alice
cn: Alice Workspace
sn: Workspace
givenName: Alice
mail: alice@lab.vc-workspace.test
employeeType: vc-workspace
uidNumber: 20001
gidNumber: 20001
homeDirectory: /home/alice
loginShell: /bin/bash
userPassword: $alice_hash

dn: uid=bob,ou=people,dc=lab,dc=vc-workspace,dc=test
objectClass: top
objectClass: person
objectClass: organizationalPerson
objectClass: inetOrgPerson
objectClass: posixAccount
objectClass: shadowAccount
uid: bob
cn: Bob Outside
sn: Outside
givenName: Bob
mail: bob@lab.vc-workspace.test
employeeType: outside
uidNumber: 20002
gidNumber: 20002
homeDirectory: /home/bob
loginShell: /bin/bash
userPassword: $bob_hash

dn: cn=workspace-users,ou=groups,dc=lab,dc=vc-workspace,dc=test
objectClass: top
objectClass: posixGroup
cn: workspace-users
gidNumber: 20001
memberUid: alice

dn: cn=outside-users,ou=groups,dc=lab,dc=vc-workspace,dc=test
objectClass: top
objectClass: posixGroup
cn: outside-users
gidNumber: 20002
memberUid: bob
EOF

rm -f "$database_dir"/*
slapadd -f "$config_file" -l "$data_file"
chown -R openldap:openldap "$state_dir" /shared
chmod 0600 "$state_dir/server.key" "$state_dir/ca.key"
chmod 0644 "$state_dir/server.crt" /shared/ca.crt

exec /usr/sbin/slapd -d 0 -f "$config_file" -h 'ldap://0.0.0.0:389/' -u openldap -g openldap
