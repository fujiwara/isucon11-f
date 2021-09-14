COMMAND := "cd /opt/mitamae && ./setup.sh && mitamae local roles/default.rb"

test:
	docker run -e SLACK_TOKEN -e NOTIFY_SLACK_SNNIPET_CHANNEL -v `pwd`:/opt/mitamae -it ubuntu:20.04 sh -c $(COMMAND)
