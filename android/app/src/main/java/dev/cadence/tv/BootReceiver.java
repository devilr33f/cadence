package dev.cadence.tv;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;

// starts the daemon at boot (delivered once the app has been launched at least once); the screen opens when the
// owner opens the app, an android app may not bring an activity up from the background
public final class BootReceiver extends BroadcastReceiver {
    @Override
    public void onReceive(Context context, Intent intent) {
        if (!Intent.ACTION_BOOT_COMPLETED.equals(intent.getAction()))
            return;
        DaemonService.start(context);
    }
}
