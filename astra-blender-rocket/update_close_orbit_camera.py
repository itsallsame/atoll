import bpy
import math
import os


ROOT = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(ROOT, "output")
COLOR_MODE = os.environ.get("SHENZHOU_COLOR_MODE") == "1"
variant = "color" if COLOR_MODE else "clay"
BLEND_OUT = os.path.join(OUT, f"shenzhou5-{variant}-launch.blend")
FRAMES_OUT = os.path.join(OUT, f"{variant}-frames")
PREVIEW_OUT = os.path.join(OUT, f"shenzhou5-{variant}-close-orbit-preview.png")

scene = bpy.context.scene
camera = bpy.data.objects["CLAY_DEMO_CAMERA"]
rocket = bpy.data.objects["ROCKET_CONTROL_ROOT"]

# A true near-field move: the last half is only a few Blender units from the
# vehicle (roughly several to tens of metres at this scene's scale). The camera
# climbs above the nose and looks down the full longitudinal axis.
camera.animation_data_clear()
camera.data.animation_data_clear()
bpy.context.preferences.edit.keyframe_new_interpolation_type = "LINEAR"
camera.data.clip_start = 0.03

shots = (
    (1, (18.0, -34.0, 18.0), 50),
    (32, (10.0, -21.0, 16.0), 42),
    (56, (3.5, -8.0, 15.5), 36),
    (78, (1.1, -1.8, 16.5), 40),
    (100, (-0.5, -0.9, 18.3), 50),
    (122, (-0.9, -0.5, 23.2), 55),
    (144, (-0.7, 0.45, 27.2), 50),
)
for frame, location, lens in shots:
    camera.location = location
    camera.keyframe_insert(data_path="location", frame=frame)
    camera.data.lens = lens
    camera.data.keyframe_insert(data_path="lens", frame=frame)

# Preserve the vertical lift curve while introducing deterministic lateral and
# angular vibration after ignition. The close camera sees this as heavy engine
# shake rather than a perfectly stable CG move.
bpy.context.preferences.edit.keyframe_new_interpolation_type = "LINEAR"
for frame in range(44, 145, 2):
    scene.frame_set(frame)
    z = rocket.location.z
    ramp = min(1.0, max(0.0, (frame - 44) / 18.0))
    dx = ramp * (0.032 * math.sin(frame * 1.73) + 0.014 * math.sin(frame * 0.47))
    dy = ramp * (0.026 * math.sin(frame * 1.31 + 0.8) + 0.010 * math.sin(frame * 0.63))
    rocket.location = (dx, dy, z)
    rocket.rotation_euler = (
        ramp * 0.0052 * math.sin(frame * 1.09),
        ramp * 0.0045 * math.sin(frame * 1.57 + 0.3),
        ramp * 0.0022 * math.sin(frame * 0.81),
    )
    rocket.keyframe_insert(data_path="location", frame=frame)
    rocket.keyframe_insert(data_path="rotation_euler", frame=frame)

scene.frame_start = 1
scene.frame_end = 144
scene.render.resolution_percentage = 50
scene.render.filepath = os.path.join(FRAMES_OUT, "frame_")
bpy.ops.wm.save_as_mainfile(filepath=BLEND_OUT)

# QA the point where the camera has crossed above the fairing and is beginning
# the close clockwise orbit.
scene.frame_set(100)
scene.render.filepath = PREVIEW_OUT
bpy.ops.render.render(write_still=True)
scene.render.filepath = os.path.join(FRAMES_OUT, "frame_")
scene.frame_set(1)
bpy.ops.wm.save_as_mainfile(filepath=BLEND_OUT)

print("CLOSE_ORBIT_PREVIEW", PREVIEW_OUT)
