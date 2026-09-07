import bpy
import os


ROOT = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(ROOT, "output")
BLEND_OUT = os.path.join(OUT, "shenzhou5-clay-launch.blend")
FRAMES_OUT = os.path.join(OUT, "clay-frames")
PREVIEW_OUT = os.path.join(OUT, "shenzhou5-clay-orbit-preview.png")

camera = bpy.data.objects["CLAY_DEMO_CAMERA"]
camera.animation_data_clear()
camera.data.animation_data_clear()
bpy.context.preferences.edit.keyframe_new_interpolation_type = "LINEAR"

shots = (
    (1, (34.0, -68.0, 15.0), 32),
    (36, (27.0, -54.0, 12.0), 35),
    (60, (17.0, -37.0, 8.0), 37),
    (84, (7.0, -23.0, 4.2), 38),
    (105, (-7.0, -23.0, 5.5), 38),
    (125, (-17.0, -17.0, 8.0), 38),
    (144, (-25.0, -5.0, 11.0), 38),
)
for frame, location, lens in shots:
    camera.location = location
    camera.keyframe_insert(data_path="location", frame=frame)
    camera.data.lens = lens
    camera.data.keyframe_insert(data_path="lens", frame=frame)

scene = bpy.context.scene
scene.frame_start = 1
scene.frame_end = 144
scene.render.resolution_percentage = 50
scene.render.filepath = os.path.join(FRAMES_OUT, "frame_")
bpy.ops.wm.save_as_mainfile(filepath=BLEND_OUT)

scene.frame_set(110)
scene.render.filepath = PREVIEW_OUT
bpy.ops.render.render(write_still=True)
scene.render.filepath = os.path.join(FRAMES_OUT, "frame_")
scene.frame_set(1)
bpy.ops.wm.save_as_mainfile(filepath=BLEND_OUT)

print("ORBIT_PREVIEW", PREVIEW_OUT)
