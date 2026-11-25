package dev.cadence.tv;

import android.content.Intent;
import android.speech.RecognitionService;
import android.speech.SpeechRecognizer;

// a voice interaction service must name a recognizer to be selectable; this one recognises nothing (speech goes
// through cadence's own server)
public final class AssistRecognitionService extends RecognitionService {
    @Override
    protected void onStartListening(Intent recognizerIntent, Callback listener) {
        try {
            listener.error(SpeechRecognizer.ERROR_NO_MATCH);
        } catch (android.os.RemoteException ignored) {
            // the caller went away
        }
    }

    @Override
    protected void onStopListening(Callback listener) {}

    @Override
    protected void onCancel(Callback listener) {}
}
