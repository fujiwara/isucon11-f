http_request 'notify_slack' do
  url 'https://github.com/catatsuy/notify_slack/releases/download/v0.4.11/notify_slack-linux-amd64.tar.gz'
  path '/tmp/notify_slack-linux-amd64.tar.gz'
  mode '0644'
  not_if 'test -x /usr/local/bin/notify_slack'
  notifies :run, 'execute[install notify_slack]'
end

execute 'install notify_slack' do
  cwd '/tmp'
  command 'tar xvf notify_slack-linux-amd64.tar.gz && install notify_slack /usr/local/bin'
  action :nothing
end

template '/etc/notify_slack.toml' do
  source 'templates/notify_slack.toml.erb'
  variables(
    token: ENV['SLACK_TOKEN'],
    url: ENV['SLACK_WEBHOOK'],
    channel: ENV['NOTIFY_SLACK_SNNIPET_CHANNEL'],
  )
end
