import 'package:flutter/material.dart';

/// The one brand token there is. Both the phone app and the desktop app build
/// their light and dark schemes from it, so the two look like one product.
const seedColor = Color(0xFFD97757);

ThemeData orchestratorTheme(Brightness brightness) => ThemeData(
  colorScheme: ColorScheme.fromSeed(
    seedColor: seedColor,
    brightness: brightness,
  ),
  useMaterial3: true,
);
