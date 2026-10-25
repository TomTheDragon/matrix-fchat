#!/bin/sh
# Entry point for the Docker image. All bridge files (config, registration,
# SQLite database, logs) live in the /data volume.

BINARY_NAME=/usr/bin/matrix-fchat

if [ -z "$GID" ]; then
	GID="$UID"
fi

fixperms() {
	chown -R "$UID:$GID" /data
}

if [ ! -f /data/config.yaml ]; then
	$BINARY_NAME -c /data/config.yaml -e
	# Listen on all interfaces, otherwise the homeserver can't reach the bridge inside the container.
	sed -i 's/^\(    hostname: \)127\.0\.0\.1$/\10.0.0.0/' /data/config.yaml
	echo "Didn't find a config file."
	echo "Copied default config file to /data/config.yaml"
	echo "Modify that config file to your liking."
	echo "Start the container again after that to generate the registration file."
	fixperms
	exit
fi

if [ ! -f /data/registration.yaml ]; then
	$BINARY_NAME -g -c /data/config.yaml -r /data/registration.yaml || exit $?
	echo "Didn't find a registration file."
	echo "Generated one for you."
	echo "See https://docs.mau.fi/bridges/general/registering-appservices.html on how to use it."
	fixperms
	exit
fi

cd /data
fixperms
exec su-exec "$UID:$GID" $BINARY_NAME
