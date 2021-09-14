package 'software-properties-common'

execute 'install openresty' do
  command <<END
curl -sL https://openresty.org/package/pubkey.gpg | apt-key add -
add-apt-repository -y "deb http://openresty.org/package/ubuntu $(lsb_release -sc) main"
apt-get update
apt-get -y install openresty
END

  not_if 'test -e /usr/bin/openresty'
end
