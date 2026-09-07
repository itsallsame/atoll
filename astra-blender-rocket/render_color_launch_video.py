import os
import runpy
import subprocess

import bpy


ROOT = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(ROOT, "output")
os.environ["SHENZHOU_COLOR_MODE"] = "1"

# Build the launch animation from the original colored rocket, then apply the
# near-field top-down camera and vibration pass used by the clay control render.
runpy.run_path(os.path.join(ROOT, "build_clay_launch_demo.py"), run_name="__main__")
runpy.run_path(os.path.join(ROOT, "update_close_orbit_camera.py"), run_name="__main__")

scene = bpy.context.scene
frames = os.path.join(OUT, "color-frames")
scene.render.filepath = os.path.join(frames, "frame_")
scene.render.image_settings.file_format = "PNG"
scene.render.resolution_percentage = 50
scene.frame_start = 1
scene.frame_end = 144
bpy.ops.wm.save_as_mainfile(filepath=os.path.join(OUT, "shenzhou5-color-launch.blend"))
bpy.ops.render.render(animation=True)

video = os.path.join(OUT, "shenzhou5-color-launch-20260906.mp4")
subprocess.run(
    [
        "/opt/homebrew/bin/ffmpeg",
        "-y",
        "-framerate",
        "24",
        "-i",
        os.path.join(frames, "frame_%04d.png"),
        "-c:v",
        "libx264",
        "-pix_fmt",
        "yuv420p",
        "-crf",
        "18",
        video,
    ],
    check=True,
)

scene.frame_set(100)
bpy.ops.render.render()
print("COLOR_VIDEO", video)

