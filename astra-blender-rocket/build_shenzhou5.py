import bpy
import math
import os
from mathutils import Vector


ROOT = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(ROOT, "output")
os.makedirs(OUT, exist_ok=True)


def clear_scene():
    bpy.ops.object.select_all(action="SELECT")
    bpy.ops.object.delete(use_global=False)


def mat(name, color, metallic=0.0, roughness=0.4, emission=0.0):
    m = bpy.data.materials.get(name) or bpy.data.materials.new(name)
    m.diffuse_color = (*color, 1.0)
    m.use_nodes = True
    bsdf = next(n for n in m.node_tree.nodes if n.type == "BSDF_PRINCIPLED")
    bsdf.inputs["Base Color"].default_value = (*color, 1.0)
    bsdf.inputs["Metallic"].default_value = metallic
    bsdf.inputs["Roughness"].default_value = roughness
    if emission:
        bsdf.inputs["Emission Color"].default_value = (*color, 1.0)
        bsdf.inputs["Emission Strength"].default_value = emission
    return m


def finish(obj, material, bevel=0.025, smooth=True):
    obj.data.materials.append(material)
    if bevel:
        mod = obj.modifiers.new("Manufactured edge", "BEVEL")
        mod.width = bevel
        mod.segments = 3
        bpy.context.view_layer.objects.active = obj
        bpy.ops.object.modifier_apply(modifier=mod.name)
    if smooth:
        for p in obj.data.polygons:
            p.use_smooth = True
    return obj


def cylinder(name, radius, depth, z, material, location_xy=(0, 0), vertices=96, bevel=0.02):
    bpy.ops.mesh.primitive_cylinder_add(
        vertices=vertices, radius=radius, depth=depth,
        location=(location_xy[0], location_xy[1], z)
    )
    bpy.context.object.name = name
    return finish(bpy.context.object, material, bevel)


def cone(name, r1, r2, depth, z, material, location_xy=(0, 0), vertices=96, bevel=0.018):
    bpy.ops.mesh.primitive_cone_add(
        vertices=vertices, radius1=r1, radius2=r2, depth=depth,
        location=(location_xy[0], location_xy[1], z)
    )
    bpy.context.object.name = name
    return finish(bpy.context.object, material, bevel)


def box(name, location, scale, material, bevel=0.01):
    bpy.ops.mesh.primitive_cube_add(location=location)
    obj = bpy.context.object
    obj.name = name
    obj.scale = scale
    bpy.ops.object.transform_apply(location=False, rotation=False, scale=True)
    return finish(obj, material, bevel, smooth=False)


def torus(name, major, minor, z, material):
    bpy.ops.mesh.primitive_torus_add(
        major_radius=major, minor_radius=minor,
        major_segments=96, minor_segments=20, location=(0, 0, z)
    )
    bpy.context.object.name = name
    return finish(bpy.context.object, material, 0, True)


def look_at(obj, target):
    obj.rotation_euler = (Vector(target) - obj.location).to_track_quat("-Z", "Y").to_euler()


def text_on_front(name, body, location, size, material, font_path=None, vertical=False):
    bpy.ops.object.text_add(location=location)
    text = bpy.context.object
    text.name = name
    text.data.body = body
    text.data.align_x = "CENTER"
    text.data.align_y = "CENTER"
    text.data.size = size
    text.data.extrude = 0.008
    text.data.bevel_depth = 0.003
    if font_path and os.path.exists(font_path):
        text.data.font = bpy.data.fonts.load(font_path, check_existing=True)
    # Text normal points toward the hero camera on the -Y side.
    text.rotation_euler = (math.pi / 2, 0, math.pi / 2 if vertical else 0)
    text.data.materials.append(material)
    return text


clear_scene()

white = mat("CZ-2F Aerospace White", (0.82, 0.85, 0.84), metallic=0.18, roughness=0.3)
warm_white = mat("Fairing Warm White", (0.95, 0.92, 0.82), metallic=0.08, roughness=0.36)
red = mat("Mission Red", (0.68, 0.015, 0.02), metallic=0.25, roughness=0.27)
blue = mat("China Aerospace Blue", (0.015, 0.16, 0.5), metallic=0.28, roughness=0.25)
black = mat("Engine Graphite", (0.012, 0.017, 0.025), metallic=0.75, roughness=0.18)
gold = mat("Flag Gold", (1.0, 0.68, 0.03), metallic=0.3, roughness=0.24, emission=0.3)
steel = mat("Launch Platform", (0.045, 0.065, 0.09), metallic=0.65, roughness=0.25)
cyan = mat("Platform Light", (0.02, 0.34, 0.9), metallic=0.1, roughness=0.2, emission=4.0)

# Approximate 1 Blender unit = 5 metres. Overall height is about 11.67 units.
# Central first and second stages.
cylinder("Core Stage 1", 0.335, 5.25, 3.05, white, bevel=0.025)
cylinder("Core Lower Red Band", 0.348, 0.34, 0.66, red, bevel=0.018)
torus("Core Lower Ring", 0.345, 0.026, 1.0, blue)
cylinder("Interstage Lattice", 0.347, 0.46, 5.73, black, bevel=0.012)
cylinder("Core Stage 2", 0.335, 2.42, 7.17, white, bevel=0.022)
torus("Upper Blue Ring", 0.346, 0.025, 8.25, blue)

# Engine cluster.
for i, (x, y) in enumerate(((0.15, 0.15), (-0.15, 0.15), (0.15, -0.15), (-0.15, -0.15)), 1):
    cone(f"Core Engine {i}", 0.105, 0.064, 0.42, 0.23, black, (x, y), bevel=0.012)

# Four strap-on boosters with tapered shoulders and nozzles.
for i, angle in enumerate((0, math.pi / 2, math.pi, math.pi * 1.5), 1):
    x, y = math.cos(angle) * 0.555, math.sin(angle) * 0.555
    cylinder(f"Booster {i}", 0.225, 3.48, 2.05, white, (x, y), bevel=0.021)
    cylinder(f"Booster {i} Red Band", 0.235, 0.3, 0.48, red, (x, y), bevel=0.014)
    cone(f"Booster {i} Shoulder", 0.225, 0.075, 0.72, 4.15, red, (x, y), bevel=0.014)
    cone(f"Booster {i} Engine", 0.15, 0.09, 0.38, 0.18, black, (x, y), bevel=0.01)

# Crew-launch fairing and launch escape system.
cylinder("Crew Fairing", 0.385, 1.5, 9.05, warm_white, bevel=0.025)
cone("Fairing Shoulder", 0.385, 0.19, 0.62, 10.11, warm_white, bevel=0.018)
cylinder("Escape Tower Base", 0.105, 0.45, 10.64, white, bevel=0.012)
cylinder("Escape Tower Mast", 0.055, 0.92, 11.3, white, bevel=0.008)
cone("Escape Tower Nose", 0.13, 0.012, 0.62, 12.05, red, bevel=0.01)

# Four small escape-tower lattice arms.
for i, angle in enumerate((0, math.pi / 2, math.pi, math.pi * 1.5), 1):
    x, y = math.cos(angle) * 0.12, math.sin(angle) * 0.12
    arm = cylinder(f"Escape Arm {i}", 0.018, 0.55, 10.77, red, (x, y), vertices=24, bevel=0.004)
    arm.rotation_euler[1] = math.radians(18)
    arm.rotation_euler[2] = angle

# Front mission markings: PRC flag and recognizable blue aerospace typography.
front_y = -0.341
box("PRC Flag", (-0.13, front_y - 0.012, 7.55), (0.17, 0.012, 0.11), red, bevel=0.008)
bpy.ops.mesh.primitive_uv_sphere_add(segments=24, ring_count=12, radius=0.055, location=(-0.19, front_y - 0.033, 7.57))
star = bpy.context.object
star.name = "Flag Emblem"
star.scale = (1.0, 0.18, 1.0)
star.data.materials.append(gold)

font_path = "/System/Library/AssetsV2/com_apple_MobileAsset_Font7/3419f2a427639ad8c8e139149a287865a90fa17e.asset/AssetData/PingFang.ttc"
# Build the traditional vertical marking as individually placed glyphs so its
# orientation remains stable across Blender font engines.
for index, glyph in enumerate("中国航天"):
    text_on_front(
        f"China Aerospace Mark {index + 1}", glyph,
        (0, front_y - 0.018, 4.85 - index * 0.48), 0.31, blue, font_path
    )
text_on_front("CZ-2F Mark", "CZ-2F", (0, front_y - 0.02, 6.65), 0.22, blue)
text_on_front("Shenzhou 5 Mark", "神舟五号", (0, -0.391, 9.1), 0.19, red, font_path)

# Display platform.
cylinder("Display Platform", 2.2, 0.24, -0.13, steel, vertices=128, bevel=0.11)
torus("Display Light Ring", 1.64, 0.028, 0.0, cyan)
text_on_front("Mission Plaque", "SHENZHOU 5   •   2003", (0, -1.82, 0.0), 0.19, white)

# Camera and studio lighting.
bpy.ops.object.camera_add(location=(14.0, -31.0, 7.4))
camera = bpy.context.object
camera.name = "Shenzhou Hero Camera"
camera.data.lens = 65
look_at(camera, (0, 0, 5.9))
bpy.context.scene.camera = camera


def area(name, location, energy, color, size, target=(0, 0, 5.5)):
    bpy.ops.object.light_add(type="AREA", location=location)
    light = bpy.context.object
    light.name = name
    light.data.energy = energy
    light.data.color = color
    light.data.shape = "DISK"
    light.data.size = size
    look_at(light, target)


area("Warm Key", (6, -8, 13), 1900, (1.0, 0.57, 0.35), 6.0)
area("Cool Fill", (-7, -4, 8), 1400, (0.25, 0.48, 1.0), 5.0)
area("Launch Rim", (3, 6, 12), 2200, (0.3, 0.55, 1.0), 4.0)

scene = bpy.context.scene
scene.world.use_nodes = True
bg = scene.world.node_tree.nodes["Background"]
bg.inputs["Color"].default_value = (0.0015, 0.004, 0.014, 1)
bg.inputs["Strength"].default_value = 0.19
scene.render.engine = "BLENDER_EEVEE"
scene.render.resolution_x = 900
scene.render.resolution_y = 1200
scene.render.resolution_percentage = 100
scene.render.image_settings.file_format = "PNG"
scene.render.filepath = os.path.join(OUT, "shenzhou5-hero.png")
scene.view_settings.look = "AgX - Medium High Contrast"

bpy.ops.wm.save_as_mainfile(filepath=os.path.join(OUT, "shenzhou5-cz2f.blend"))
bpy.ops.render.render(write_still=True)
print("SHENZHOU5_OUTPUT", os.path.join(OUT, "shenzhou5-cz2f.blend"))
