http_request 'alp' do
  url 'https://github.com/tkuchiki/alp/releases/download/v1.0.7/alp_linux_amd64.zip'
  path '/tmp/alp_linux_amd64.zip'
  mode '0644'
  not_if 'test -x /usr/local/bin/alp'
  notifies :run, 'execute[install alp]'
end

execute 'install alp' do
  cwd '/tmp'
  command 'unzip /tmp/alp_linux_amd64.zip && install alp /usr/local/bin'
  action :nothing
end
