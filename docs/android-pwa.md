# Mobile Browsers and PWA

The same NexBox web interface works in desktop and mobile browsers. For server deployments, open the instance's address. For local deployments, `127.0.0.1` refers to the current device; choose a listening address or tunnel appropriate to how you connect.

The frontend includes `manifest.webmanifest`, app icons, and standalone window display settings. Supported browsers may offer an "Add to Home Screen" or "Install App" option. Availability depends on the browser and access environment.

There is no offline execution mode: the browser must remain connected to the NexBox service, and conversations run through CLIs on the service's machine. Installing the PWA does not install an agent on your phone, and it is not an Electron or native client.

Android Trusted Web Activity (TWA) domain association depends on the deployment's domain, Android package name, and signing certificate. Configure your own association through `public_web.android_asset_links` in the instance's `config.json`; see the [operations guide](ops_manual.md#optional-site-verification-and-android-twa). NexBox does not provide a general-purpose APK build or release workflow.
