Name:           mountmgr-agent
Version:        @VERSION@
Release:        1%{?dist}
Summary:        Mount Manager client agent
License:        Proprietary
Source0:        %{name}-%{version}.tar.gz
BuildRequires:  gcc, make, bash, coreutils, libcurl-devel, openssl-devel, systemd-rpm-macros
Requires:       nfs-utils, sudo, libcurl, openssl-libs
%{?systemd_requires}

%description
Lightweight daemon that polls a Mount Manager server over mTLS and reconciles
NFS mounts on this host, reporting state centrally and auditing to syslog.

%prep
%autosetup

%build
make %{?_smp_mflags} VERSION=%{version} CFLAGS="%{optflags} -Wno-deprecated-declarations"

%check
make check VERSION=%{version}

%install
install -D -m 0755 mmd %{buildroot}%{_sbindir}/mmd
install -D -m 0644 mountmgr-agent.service %{buildroot}%{_unitdir}/mountmgr-agent.service
install -D -m 0640 agent.conf %{buildroot}%{_sysconfdir}/mountmgr/agent.conf
install -D -m 0440 mountmgr.sudoers %{buildroot}%{_sysconfdir}/sudoers.d/mountmgr
install -d -m 0700 %{buildroot}%{_sharedstatedir}/mountmgr

%pre
getent group mountmgr >/dev/null || groupadd -r mountmgr
getent passwd mountmgr >/dev/null || useradd -r -g mountmgr -d %{_sharedstatedir}/mountmgr -s /sbin/nologin -c "Mount Manager agent" mountmgr
exit 0

%post
%systemd_post mountmgr-agent.service

%preun
%systemd_preun mountmgr-agent.service

%postun
%systemd_postun_with_restart mountmgr-agent.service

%files
%{_sbindir}/mmd
%{_unitdir}/mountmgr-agent.service
%dir %attr(0750,root,mountmgr) %{_sysconfdir}/mountmgr
%config(noreplace) %attr(0640,root,mountmgr) %{_sysconfdir}/mountmgr/agent.conf
# package-owned (replaced on upgrade); local changes belong in a separate sudoers.d file
%attr(0440,root,root) %{_sysconfdir}/sudoers.d/mountmgr
%dir %attr(0700,mountmgr,mountmgr) %{_sharedstatedir}/mountmgr

%changelog
@CHANGELOG@
