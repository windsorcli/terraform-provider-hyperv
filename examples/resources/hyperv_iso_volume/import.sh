# Import an iso_volume by its destination path. Imported resources land
# with empty volume_label and an empty files map -- those values aren't
# reconstructible from the on-disk bytes. The next plan will surface a
# sha256 diff and re-stream once the user adds the canonical config,
# recovering full state.
terraform import hyperv_iso_volume.ubuntu_seed "C:/hyperv/seeds/ubuntu-22.04-cidata.iso"
