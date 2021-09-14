http_request 'netdata-installer' do
  url 'https://my-netdata.io/kickstart-static64.sh'
  path '/tmp/kickstart-static64.sh'
  mode '0755'
  notifies :run, 'execute[install netdata]'
  not_if 'test -d /opt/netdata'
end

execute 'install netdata' do
  cwd '/tmp'
  command 'echo y | ./kickstart-static64.sh'
  action :nothing
end
