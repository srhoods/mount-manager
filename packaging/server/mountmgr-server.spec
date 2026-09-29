%global debug_package %{nil}
%global __strip /bin/true
Name:           mountmgr-server
Version:        @VERSION@
Release:        1%{?dist}
Summary:        Mount Manager server
License:        Proprietary
Source0:        %{name}-%{version}.tar.gz
BuildRequires:  systemd-rpm-macros
%{?systemd_requires}

%description
Mount Manager server: agent mTLS API with built-in CA, admin REST API and
PostgreSQL-backed configuration and audit store.

%prep
%autosetup

%install
install -D -m 0755 mmserver %{buildroot}%{_sbindir}/mmserver
install -D -m 0644 mountmgr-server.service %{buildroot}%{_unitdir}/mountmgr-server.service
install -D -m 0640 server.env %{buildroot}%{_sysconfdir}/mountmgr-server/server.env
install -D -m 0640 ldap.json.example %{buildroot}%{_sysconfdir}/mountmgr-server/ldap.json.example

%pre
getent group mmserver >/dev/null || groupadd -r mmserver
getent passwd mmserver >/dev/null || useradd -r -g mmserver -d /nonexistent -s /sbin/nologin -c "Mount Manager server" mmserver
exit 0

%post
%systemd_post mountmgr-server.service
if [ $1 -eq 1 ]; then
  echo "mountmgr-server: edit /etc/mountmgr-server/server.env, then create the first admin with:"
  echo "  set -a; . /etc/mountmgr-server/server.env; set +a; runuser -u mmserver -- mmserver -init-admin '<password>'"
  echo "  systemctl enable --now mountmgr-server   (open TCP 8443 and 8444 in firewalld if enabled)"
fi

%preun
%systemd_preun mountmgr-server.service

%postun
%systemd_postun_with_restart mountmgr-server.service

%files
%{_sbindir}/mmserver
%{_unitdir}/mountmgr-server.service
%dir %attr(0750,root,mmserver) %{_sysconfdir}/mountmgr-server
%config(noreplace) %attr(0640,root,mmserver) %{_sysconfdir}/mountmgr-server/server.env
%attr(0640,root,mmserver) %{_sysconfdir}/mountmgr-server/ldap.json.example

%changelog
@CHANGELOG@
