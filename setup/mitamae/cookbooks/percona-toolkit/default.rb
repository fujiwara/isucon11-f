http_request 'percona-release_latest.generic_all.deb' do
  url 'https://repo.percona.com/apt/percona-release_latest.generic_all.deb'
  path '/tmp/percona-release_latest.generic_all.deb'
  mode '0644'
  notifies :run, 'execute[install percona-toolkit]'
  not_if 'dpkg --list percona-toolkit'
end

execute 'install percona-toolkit' do
  cwd '/tmp'
  command 'dpkg -i percona-release_latest.generic_all.deb && apt-get -y install percona-toolkit'
  action :nothing
end
