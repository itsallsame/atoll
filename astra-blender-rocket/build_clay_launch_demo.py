import bpy
import math
import os
from mathutils import Vector


ROOT = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(ROOT, "output")
SOURCE = os.path.join(OUT, "shenzhou5-cz2f.blend")
COLOR_MODE = os.environ.get("SHENZHOU_COLOR_MODE") == "1"
variant = "color" if COLOR_MODE else "clay"
BLEND_OUT = os.path.join(OUT, f"shenzhou5-{variant}-launch.blend")
VIDEO_OUT = os.path.join(OUT, f"shenzhou5-{variant}-launch.mp4")
PREVIEW_OUT = os.path.join(OUT, f"shenzhou5-{variant}-preview.png")
FRAMES_OUT = os.path.join(OUT, f"{variant}-frames")


def material(name, color, roughness=0.72, emission=0.0):
    mat = bpy.data.materials.get(name) or bpy.data.materials.new(name)
    mat.use_nodes = True
    bsdf = next(node for node in mat.node_tree.nodes if node.type == "BSDF_PRINCIPLED")
    bsdf.inputs["Base Color"].default_value = (*color, 1.0)
    bsdf.inputs["Roughness"].default_value = roughness
    bsdf.inputs["Metallic"].default_value = 0.0
    if emission:
        bsdf.inputs["Emission Color"].default_value = (*color, 1.0)
        bsdf.inputs["Emission Strength"].default_value = emission
    mat.diffuse_color = (*color, 1.0)
    return mat


def set_material(obj, mat):
    if not hasattr(obj.data, "materials"):
        return
    obj.data.materials.clear()
    obj.data.materials.append(mat)


def look_at(obj, target):
    obj.rotation_euler = (Vector(target) - obj.location).to_track_quat("-Z", "Y").to_euler()


def keyframe(obj, frame, location=None, scale=None):
    if location is not None:
        obj.location = location
        obj.keyframe_insert(data_path="location", frame=frame)
    if scale is not None:
        obj.scale = scale
        obj.keyframe_insert(data_path="scale", frame=frame)


def make_uv_sphere(name, location, scale, mat):
    bpy.ops.mesh.primitive_ico_sphere_add(subdivisions=2, radius=1.0, location=location)
    obj = bpy.context.object
    obj.name = name
    obj.scale = scale
    set_material(obj, mat)
    return obj


if not os.path.exists(SOURCE):
    raise FileNotFoundError(SOURCE)

# Loading a .blend from Blender's interactive Python Console replaces the
# console context and stops the remainder of the command. Open the source only
# for background use; when launched from the GUI, open SOURCE first and rerun.
if os.path.abspath(bpy.data.filepath) != os.path.abspath(SOURCE):
    bpy.ops.wm.open_mainfile(filepath=SOURCE)
    if not bpy.app.background:
        raise RuntimeError(
            "Source scene loaded. Run this script once more from the Python Console."
        )

clay = material("Clay Ivory", (0.72, 0.70, 0.65))
dark_clay = material("Clay Charcoal", (0.075, 0.082, 0.09), roughness=0.86)
ground_mat = material(
    "Launch Ground" if COLOR_MODE else "Clay Ground",
    (0.055, 0.065, 0.075) if COLOR_MODE else (0.16, 0.17, 0.18),
    roughness=0.94,
)
plume_mat = material(
    "Hot Engine Plume" if COLOR_MODE else "Plume Guide",
    (1.0, 0.22, 0.025) if COLOR_MODE else (0.94, 0.91, 0.82),
    roughness=0.3 if COLOR_MODE else 0.45,
    emission=7.0 if COLOR_MODE else 0.35,
)
smoke_mat = material(
    "Launch Smoke" if COLOR_MODE else "Smoke Guide",
    (0.12, 0.14, 0.17) if COLOR_MODE else (0.38, 0.39, 0.40),
    roughness=1.0,
)

# Remove the product-shot platform, labels and old lights. Keep the rocket mesh.
for obj in list(bpy.data.objects):
    if obj.type in {"LIGHT", "CAMERA"} or obj.name in {
        "Display Platform", "Display Light Ring", "Mission Plaque"
    } or obj.type == "FONT":
        bpy.data.objects.remove(obj, do_unlink=True)

# All original rocket geometry becomes one neutral clay subject controlled by a root.
rocket_root = bpy.data.objects.new("ROCKET_CONTROL_ROOT", None)
bpy.context.scene.collection.objects.link(rocket_root)
for obj in list(bpy.context.scene.objects):
    if obj == rocket_root or obj.type != "MESH":
        continue
    if not COLOR_MODE:
        if obj.name not in {"PRC Flag", "Flag Emblem"}:
            set_material(obj, clay)
        else:
            set_material(obj, dark_clay)
    obj.parent = rocket_root

# Simple launch ground and four silhouette towers create stable depth cues.
bpy.ops.mesh.primitive_plane_add(size=70, location=(0, 0, -0.27))
ground = bpy.context.object
ground.name = "CLAY_LAUNCH_GROUND"
set_material(ground, ground_mat)

for index, (x, y) in enumerate(((-4.8, 2.7), (4.8, 2.7), (-5.8, 7.5), (5.8, 7.5)), 1):
    bpy.ops.mesh.primitive_cube_add(location=(x, y, 3.1), scale=(0.22, 0.22, 3.35))
    tower = bpy.context.object
    tower.name = f"CLAY_TOWER_{index}"
    set_material(tower, dark_clay)
    for level in range(1, 6):
        bpy.ops.mesh.primitive_cube_add(
            location=(x, y, level * 1.05), scale=(0.72, 0.16, 0.07)
        )
        beam = bpy.context.object
        beam.name = f"CLAY_TOWER_{index}_BEAM_{level}"
        set_material(beam, dark_clay)

# One geometric plume under the engine cluster. It moves with the rocket.
bpy.ops.mesh.primitive_cone_add(vertices=48, radius1=0.76, radius2=0.22, depth=3.4, location=(0, 0, -1.72))
plume = bpy.context.object
plume.name = "PLUME_MOTION_GUIDE"
set_material(plume, plume_mat)
plume.parent = rocket_root
keyframe(plume, 1, scale=(0.08, 0.08, 0.08))
keyframe(plume, 34, scale=(0.08, 0.08, 0.08))
keyframe(plume, 48, scale=(0.65, 0.65, 0.75))
keyframe(plume, 72, scale=(1.0, 1.0, 1.2))
keyframe(plume, 144, scale=(1.15, 1.15, 1.55))

# Expanding smoke blobs remain near the pad to communicate ignition and lift-off.
smoke_specs = [
    ((-0.8, 0.0, 0.0), (2.8, 1.7, 0.7)),
    ((0.9, 0.1, 0.05), (3.2, 1.5, 0.8)),
    ((-1.8, 0.6, 0.1), (3.8, 2.2, 1.0)),
    ((1.9, 0.8, 0.15), (4.0, 2.0, 1.1)),
    ((0.0, 1.5, 0.1), (4.2, 2.8, 1.2)),
]
for index, (location, final_scale) in enumerate(smoke_specs, 1):
    puff = make_uv_sphere(f"SMOKE_MOTION_GUIDE_{index}", location, (0.02, 0.02, 0.02), smoke_mat)
    keyframe(puff, 1, scale=(0.02, 0.02, 0.02))
    keyframe(puff, 38 + index * 2, scale=(0.02, 0.02, 0.02))
    keyframe(puff, 76, location=(location[0] * 1.25, location[1], location[2] + 0.45), scale=final_scale)
    keyframe(
        puff, 144,
        location=(location[0] * 1.65, location[1] + 0.8, location[2] + 1.25),
        scale=tuple(value * 1.55 for value in final_scale),
    )

# Hold for 1.5 seconds, then lift 14 Blender units with a smooth acceleration.
keyframe(rocket_root, 1, location=(0, 0, 0))
keyframe(rocket_root, 36, location=(0, 0, 0))
keyframe(rocket_root, 54, location=(0, 0, 0.35))
keyframe(rocket_root, 92, location=(0, 0, 4.1))
keyframe(rocket_root, 144, location=(0, 0, 14.0))

# Animate a target rather than baking camera rotations: the framing follows the ascent.
target = bpy.data.objects.new("CAMERA_TARGET", None)
bpy.context.scene.collection.objects.link(target)
keyframe(target, 1, location=(0, 0, 5.3))
keyframe(target, 54, location=(0, 0, 5.6))
keyframe(target, 92, location=(0, 0, 9.6))
keyframe(target, 144, location=(0, 0, 19.5))

bpy.ops.object.camera_add(location=(34.0, -68.0, 15.0))
camera = bpy.context.object
camera.name = "CLAY_DEMO_CAMERA"
camera.data.lens = 32
camera.data.sensor_width = 36
constraint = camera.constraints.new(type="TRACK_TO")
constraint.target = target
constraint.track_axis = "TRACK_NEGATIVE_Z"
constraint.up_axis = "UP_Y"
bpy.context.preferences.edit.keyframe_new_interpolation_type = "LINEAR"
keyframe(camera, 1, location=(34.0, -68.0, 15.0))
camera.data.lens = 32
camera.data.keyframe_insert(data_path="lens", frame=1)
keyframe(camera, 36, location=(27.0, -54.0, 12.0))
camera.data.lens = 35
camera.data.keyframe_insert(data_path="lens", frame=36)
keyframe(camera, 60, location=(17.0, -37.0, 8.0))
camera.data.lens = 37
camera.data.keyframe_insert(data_path="lens", frame=60)
keyframe(camera, 84, location=(7.0, -23.0, 4.2))
camera.data.lens = 38
camera.data.keyframe_insert(data_path="lens", frame=84)
keyframe(camera, 105, location=(-7.0, -23.0, 5.5))
camera.data.lens = 38
camera.data.keyframe_insert(data_path="lens", frame=105)
keyframe(camera, 125, location=(-17.0, -17.0, 8.0))
camera.data.lens = 38
camera.data.keyframe_insert(data_path="lens", frame=125)
keyframe(camera, 144, location=(-25.0, -5.0, 11.0))
camera.data.lens = 38
camera.data.keyframe_insert(data_path="lens", frame=144)
bpy.context.scene.camera = camera

# Broad neutral lighting keeps the control render readable without imposing a final style.
def area(name, location, energy, size, target_position):
    bpy.ops.object.light_add(type="AREA", location=location)
    light = bpy.context.object
    light.name = name
    light.data.energy = energy
    light.data.shape = "DISK"
    light.data.size = size
    look_at(light, target_position)


area("CLAY_KEY", (-8, -10, 16), 2100, 8.0, (0, 0, 6))
area("CLAY_FILL", (10, -4, 10), 1200, 7.0, (0, 0, 6))
area("CLAY_RIM", (0, 10, 14), 1800, 6.0, (0, 0, 7))

scene = bpy.context.scene
scene.frame_start = 1
scene.frame_end = 144
scene.render.fps = 24
scene.render.engine = "BLENDER_EEVEE"
scene.render.resolution_x = 1280
scene.render.resolution_y = 720
scene.render.resolution_percentage = 100
scene.render.image_settings.file_format = "PNG"
os.makedirs(FRAMES_OUT, exist_ok=True)
scene.render.filepath = os.path.join(FRAMES_OUT, "frame_")
scene.view_settings.look = "AgX - Medium High Contrast"

scene.world.use_nodes = True
background = scene.world.node_tree.nodes.get("Background")
background.inputs["Color"].default_value = (0.055, 0.065, 0.08, 1.0)
background.inputs["Strength"].default_value = 0.32

bpy.ops.wm.save_as_mainfile(filepath=BLEND_OUT)

# Render one representative frame for fast visual QA. The full video is rendered
# by passing --render-anim after this script.
scene.frame_set(82)
scene.render.filepath = PREVIEW_OUT
bpy.ops.render.render(write_still=True)
scene.render.filepath = os.path.join(FRAMES_OUT, "frame_")
bpy.ops.wm.save_as_mainfile(filepath=BLEND_OUT)

print("CLAY_BLEND", BLEND_OUT)
print("CLAY_PREVIEW", PREVIEW_OUT)
print("CLAY_VIDEO", VIDEO_OUT)
print("CLAY_FRAMES", FRAMES_OUT)
