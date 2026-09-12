d-i debian-installer/locale string en_US.UTF-8
d-i keyboard-configuration/xkb-keymap select us
d-i netcfg/choose_interface select auto
d-i netcfg/get_hostname string vc-workspace-debian-13-xfce
d-i netcfg/get_domain string local

d-i mirror/country string manual
d-i mirror/protocol string ${mirror_protocol}
d-i mirror/http/hostname string ${mirror_host}
d-i mirror/http/directory string ${mirror_directory}
d-i mirror/http/proxy string
d-i mirror/suite string trixie

d-i passwd/root-login boolean false
d-i passwd/user-fullname string VC Workspace Image Builder
d-i passwd/username string vdi-builder
d-i passwd/user-password password ${builder_password}
d-i passwd/user-password-again password ${builder_password}
d-i user-setup/allow-password-weak boolean true

d-i clock-setup/utc boolean true
d-i time/zone string UTC
d-i clock-setup/ntp boolean true

d-i partman-auto/method string regular
d-i partman-auto/disk string /dev/sda
d-i partman-auto/choose_recipe select atomic
d-i partman-partitioning/confirm_write_new_label boolean true
d-i partman/choose_partition select finish
d-i partman/confirm boolean true
d-i partman/confirm_nooverwrite boolean true

tasksel tasksel/first multiselect standard, ssh-server
d-i pkgsel/include string sudo curl ca-certificates qemu-guest-agent cloud-init
d-i pkgsel/upgrade select none
popularity-contest popularity-contest/participate boolean false

d-i grub-installer/only_debian boolean true
d-i grub-installer/bootdev string /dev/sda
grub-pc grub-pc/install_devices multiselect /dev/sda
grub-pc grub-pc/install_devices_empty boolean false
d-i preseed/late_command string in-target sh -c 'echo "vdi-builder ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/vdi-builder'; in-target chmod 0440 /etc/sudoers.d/vdi-builder; in-target grub-install /dev/sda; in-target update-grub
d-i finish-install/reboot_in_progress note
