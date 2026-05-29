package xyz.cloudhelper.probenode;

import android.app.Activity;
import android.content.Intent;
import android.net.Uri;
import android.provider.Settings;

import androidx.core.content.FileProvider;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.File;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.Locale;

public final class AndroidUpgrade {
    public static final String PLATFORM = "android";
    public static final String ARCH = "arm64";
    public static final String ASSET_NAME = "cloudhelper-probe-node-android-arm64.apk";
    public static final String DEFAULT_RELEASE_API = "https://api.github.com/repos/fengzhanhuaer/CloudHelper/releases/latest";

    public interface StatusSink {
        void onStatus(String message);
    }

    private AndroidUpgrade() {
    }

    public static boolean matchesAsset(String name) {
        String value = name == null ? "" : name.trim().toLowerCase(Locale.ROOT);
        return value.equals(ASSET_NAME)
                || (value.contains("probe-node")
                && value.contains(PLATFORM)
                && value.contains(ARCH)
                && value.endsWith(".apk"));
    }

    public static String describeUpgradeChannel() {
        return "Upgrade channel: " + ASSET_NAME;
    }

    public static void checkDownloadAndInstall(Activity activity, StatusSink sink) {
        new Thread(() -> {
            try {
                emit(sink, "Checking latest Android APK...");
                Asset asset = fetchLatestAndroidAsset();
                if (asset == null) {
                    emit(sink, "No Android arm64 APK asset found.");
                    return;
                }
                emit(sink, "Downloading " + asset.name + "...");
                File apk = downloadAsset(activity, asset);
                emit(sink, "Opening Android installer...");
                openInstaller(activity, apk);
                emit(sink, "Installer opened for " + asset.name + ".");
            } catch (Exception e) {
                emit(sink, "Upgrade failed: " + e.getMessage());
            }
        }, "cloudhelper-android-upgrade").start();
    }

    private static Asset fetchLatestAndroidAsset() throws Exception {
        HttpURLConnection conn = (HttpURLConnection) new URL(DEFAULT_RELEASE_API).openConnection();
        conn.setRequestProperty("Accept", "application/vnd.github+json");
        conn.setRequestProperty("User-Agent", "cloudhelper-probe-node-android");
        conn.setConnectTimeout(12000);
        conn.setReadTimeout(12000);
        if (conn.getResponseCode() < 200 || conn.getResponseCode() >= 300) {
            throw new IllegalStateException("release api status=" + conn.getResponseCode());
        }
        String body = readAll(conn.getInputStream());
        JSONArray assets = new JSONObject(body).optJSONArray("assets");
        if (assets == null) {
            return null;
        }
        for (int i = 0; i < assets.length(); i++) {
            JSONObject item = assets.getJSONObject(i);
            String name = item.optString("name", "");
            if (!matchesAsset(name)) {
                continue;
            }
            String url = item.optString("browser_download_url", "");
            if (url.trim().isEmpty()) {
                continue;
            }
            return new Asset(name, url);
        }
        return null;
    }

    private static File downloadAsset(Activity activity, Asset asset) throws Exception {
        File dir = new File(activity.getCacheDir(), "upgrades");
        if (!dir.exists() && !dir.mkdirs()) {
            throw new IllegalStateException("failed to create upgrade cache");
        }
        File apk = new File(dir, ASSET_NAME);
        File part = new File(dir, ASSET_NAME + ".part");

        HttpURLConnection conn = (HttpURLConnection) new URL(asset.url).openConnection();
        conn.setRequestProperty("Accept", "application/octet-stream");
        conn.setRequestProperty("User-Agent", "cloudhelper-probe-node-android");
        conn.setConnectTimeout(15000);
        conn.setReadTimeout(60000);
        if (conn.getResponseCode() < 200 || conn.getResponseCode() >= 300) {
            throw new IllegalStateException("apk download status=" + conn.getResponseCode());
        }

        try (InputStream in = conn.getInputStream(); FileOutputStream out = new FileOutputStream(part, false)) {
            byte[] buf = new byte[64 * 1024];
            int n;
            while ((n = in.read(buf)) >= 0) {
                out.write(buf, 0, n);
            }
        }
        if (apk.exists() && !apk.delete()) {
            throw new IllegalStateException("failed to replace old apk");
        }
        if (!part.renameTo(apk)) {
            throw new IllegalStateException("failed to stage apk");
        }
        return apk;
    }

    private static void openInstaller(Activity activity, File apk) {
        if (!activity.getPackageManager().canRequestPackageInstalls()) {
            Intent settingsIntent = new Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES);
            settingsIntent.setData(Uri.parse("package:" + activity.getPackageName()));
            activity.runOnUiThread(() -> activity.startActivity(settingsIntent));
            throw new IllegalStateException("please allow installing unknown apps, then retry");
        }

        Uri uri = FileProvider.getUriForFile(activity, activity.getPackageName() + ".files", apk);
        Intent intent = new Intent(Intent.ACTION_VIEW);
        intent.setDataAndType(uri, "application/vnd.android.package-archive");
        intent.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);
        intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
        activity.runOnUiThread(() -> activity.startActivity(intent));
    }

    private static String readAll(InputStream in) throws Exception {
        StringBuilder out = new StringBuilder();
        byte[] buf = new byte[16 * 1024];
        int n;
        while ((n = in.read(buf)) >= 0) {
            out.append(new String(buf, 0, n, StandardCharsets.UTF_8));
        }
        return out.toString();
    }

    private static void emit(StatusSink sink, String message) {
        if (sink != null) {
            sink.onStatus(message);
        }
    }

    private static final class Asset {
        final String name;
        final String url;

        Asset(String name, String url) {
            this.name = name;
            this.url = url;
        }
    }
}
