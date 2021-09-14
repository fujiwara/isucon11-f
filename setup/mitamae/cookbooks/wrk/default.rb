git '/tmp/wrk' do
  repository 'https://github.com/wg/wrk.git'
  not_if 'test -e /usr/local/bin/wrk'
  notifies :run, 'execute[install wrk]'
end

execute 'install wrk' do
  cwd '/tmp/wrk'
  command 'make && install wrk /usr/local/bin/wrk'
  action :nothing
end
