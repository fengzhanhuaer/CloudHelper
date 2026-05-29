package xyz.cloudhelper.probenode;

import android.app.Activity;
import android.os.Bundle;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.TextView;

public class MainActivity extends Activity {
    private TextView statusView;
    private Button upgradeButton;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        int padding = dp(20);
        root.setPadding(padding, padding, padding, padding);

        TextView title = new TextView(this);
        title.setText("CloudHelper Probe Node");
        title.setTextSize(22);
        root.addView(title);

        statusView = new TextView(this);
        statusView.setText("Android arm64 client shell is ready.");
        statusView.setPadding(0, dp(12), 0, dp(12));
        root.addView(statusView);

        upgradeButton = new Button(this);
        upgradeButton.setText("Check Upgrade");
        upgradeButton.setOnClickListener(v -> {
            upgradeButton.setEnabled(false);
            AndroidUpgrade.checkDownloadAndInstall(this, message -> runOnUiThread(() -> {
                statusView.setText(message);
                upgradeButton.setEnabled(true);
            }));
        });
        root.addView(upgradeButton);

        setContentView(root);
    }

    private int dp(int value) {
        return Math.round(value * getResources().getDisplayMetrics().density);
    }
}
